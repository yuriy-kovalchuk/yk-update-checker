package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// MaxDependencies caps the references one repository may store (~1.3 KB in etcd and ~2 KB of dashboard heap each).
const MaxDependencies = 2000

// Writer records scan outcomes in a Backend.
type Writer struct {
	Backend  Backend
	History  int    // runs kept per repository
	Instance string // LabelInstance value on every object written; empty for none
}

// Repo records one repository's scan; a failed scan keeps its previous dependencies.
func (w Writer) Repo(ctx context.Context, repo string, results []scan.Result, scanErr error) error {
	now := time.Now().UTC()
	rs, err := w.repoScan(ctx, repo)
	if err != nil {
		return err
	}
	if scanErr == nil && len(results) > MaxDependencies {
		scanErr = errkind.Wrap(errkind.TooLarge, fmt.Errorf("%d references, more than the %d a repository may store", len(results), MaxDependencies))
	}
	st := &rs.Status
	st.Repo, st.LastScanTime, st.Error = repo, now, nil
	if scanErr != nil {
		st.Phase = PhaseFailed
		st.Error = &ErrorInfo{Kind: errkind.Of(scanErr), Message: scanErr.Error()}
	} else {
		if st.Counts, err = w.dependencies(ctx, repo, results, now); err != nil {
			return err
		}
		st.Phase, st.LastSuccessTime = PhaseSucceeded, &now
	}
	rs.Meta.Labels = w.labels(nil)
	run := Run{Time: now, Phase: st.Phase, Counts: st.Counts}
	if st.Error != nil {
		run.ErrorKind = st.Error.Kind
	}
	st.History = append([]Run{run}, st.History...)[:min(len(st.History)+1, max(w.History, 0))]
	return w.Backend.PutRepositoryScan(ctx, rs)
}

// Prune deletes the reports of repositories that are no longer configured.
func (w Writer) Prune(ctx context.Context, repos []string) error {
	keep := map[string]bool{}
	for _, r := range repos {
		keep[RepositoryScanName(r)] = true
	}
	all, err := w.Backend.ListRepositoryScans(ctx)
	if err != nil {
		return err
	}
	for _, rs := range all {
		if keep[rs.Meta.Name] {
			continue
		}
		deps, err := w.Backend.ListDependencies(ctx, RepoID(rs.Status.Repo))
		if err != nil {
			return err
		}
		for _, d := range deps {
			if err := w.Backend.DeleteDependency(ctx, d.Meta.Name); err != nil {
				return err
			}
		}
		if err := w.Backend.DeleteRepositoryScan(ctx, rs.Meta.Name); err != nil {
			return err
		}
	}
	return nil
}

func (w Writer) repoScan(ctx context.Context, repo string) (*RepositoryScan, error) {
	name := RepositoryScanName(repo)
	all, err := w.Backend.ListRepositoryScans(ctx)
	for i := range all {
		if all[i].Meta.Name == name {
			return &all[i], err
		}
	}
	return &RepositoryScan{Meta: Meta{Name: name}}, err
}

// dependencies writes the references that changed and deletes the ones that are gone.
func (w Writer) dependencies(ctx context.Context, repo string, results []scan.Result, now time.Time) (Counts, error) {
	id := RepoID(repo)
	stored, err := w.Backend.ListDependencies(ctx, id)
	if err != nil {
		return Counts{}, err
	}
	old := make(map[string]Dependency, len(stored))
	for _, d := range stored {
		old[d.Meta.Name] = d
	}
	var counts Counts
	seen := map[string]bool{}
	for _, r := range results {
		d := next(r, old, now)
		d.Meta.Labels = w.labels(map[string]string{LabelRepoID: id, LabelType: r.Type, LabelOutdated: strconv.FormatBool(r.UpdateAvailable)})
		counts.Checked++
		if r.UpdateAvailable {
			counts.Outdated++
		}
		if r.CheckError != "" {
			counts.Failed++
		}
		seen[d.Meta.Name] = true
		if prev, ok := old[d.Meta.Name]; ok && same(prev, d) {
			continue
		}
		if err := w.Backend.PutDependency(ctx, &d); err != nil {
			return counts, err
		}
	}
	for name := range old {
		if !seen[name] {
			if err := w.Backend.DeleteDependency(ctx, name); err != nil {
				return counts, err
			}
		}
	}
	return counts, nil
}

// labels adds the instance label to l.
func (w Writer) labels(l map[string]string) map[string]string {
	if w.Instance == "" {
		return l
	}
	if l == nil {
		l = map[string]string{}
	}
	l[LabelInstance] = w.Instance
	return l
}

// next builds a reference's new state, carrying over what only history knows.
func next(r scan.Result, old map[string]Dependency, now time.Time) Dependency {
	d := Dependency{Meta: Meta{Name: DependencyName(r)}, Status: DepStatus{Result: r}}
	prev, had := old[d.Meta.Name]
	if had && prev.Status.ImageDigest != "" && r.ImageDigest != "" && prev.Status.ImageDigest != r.ImageDigest {
		d.Status.DigestChanged = true
	}
	if r.UpdateAvailable {
		d.Status.OutdatedSince = &now
		if had && prev.Status.OutdatedSince != nil {
			d.Status.OutdatedSince = prev.Status.OutdatedSince
		}
	}
	if r.LatestVersion != "" {
		d.Status.LatestSeenAt = &now
		if had && prev.Status.LatestVersion == r.LatestVersion && prev.Status.LatestSeenAt != nil {
			d.Status.LatestSeenAt = prev.Status.LatestSeenAt
		}
	}
	return d
}

// same reports whether a rescan changed nothing but the check time.
func same(prev, d Dependency) bool {
	prev.Status.CheckedAt = d.Status.CheckedAt
	a, _ := json.Marshal(prev)
	b, _ := json.Marshal(d)
	return bytes.Equal(a, b)
}
