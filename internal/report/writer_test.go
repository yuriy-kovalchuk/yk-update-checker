package report

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

func res(dep, cur, latest, digest string) scan.Result {
	return scan.Result{Source: "homelab", Type: "image", Path: "app.yaml", Dependency: dep,
		CurrentVersion: cur, LatestVersion: latest, UpdateAvailable: latest != "", ImageDigest: digest}
}

func TestWriterRecordsAndUpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	f := Files{Dir: t.TempDir()}
	w := Writer{Backend: f, History: 2}

	if err := w.Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "1.1", "sha:1"), res("b", "2.0", "", "")}, nil); err != nil {
		t.Fatal(err)
	}
	first, _ := f.ListDependencies(ctx, RepoID("homelab"))
	since := map[string]*metav1.Time{}
	for _, d := range first {
		since[d.Status.Dependency] = d.Status.OutdatedSince
	}
	stamp := modTime(t, f, "b", first)

	// Rescan: a's digest moved, b unchanged, c new; nothing for b should be rewritten.
	if err := w.Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "1.1", "sha:2"), res("b", "2.0", "", ""), res("c", "3.0", "", "")}, nil); err != nil {
		t.Fatal(err)
	}
	deps, _ := f.ListDependencies(ctx, RepoID("homelab"))
	byDep := map[string]Dependency{}
	for _, d := range deps {
		byDep[d.Status.Dependency] = d
	}
	if len(byDep) != 3 {
		t.Fatalf("dependencies = %d, want 3", len(byDep))
	}
	if a := byDep["a"]; !a.Status.DigestChanged || a.Status.OutdatedSince == nil || !a.Status.OutdatedSince.Equal(since["a"]) {
		t.Errorf("a = %+v, want drift flagged and outdated_since kept from the first scan", a.Status)
	}
	if modTime(t, f, "b", deps) != stamp {
		t.Error("an unchanged dependency was rewritten")
	}

	// A removed reference disappears; history is capped.
	if err := w.Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "1.1", "sha:2")}, nil); err != nil {
		t.Fatal(err)
	}
	if deps, _ := f.ListDependencies(ctx, ""); len(deps) != 1 {
		t.Errorf("dependencies after removal = %d, want 1", len(deps))
	}
	repos, _ := f.ListRepositoryScans(ctx)
	if len(repos) != 1 || len(repos[0].Status.History) != 2 || repos[0].Status.Phase != PhaseSucceeded {
		t.Errorf("repository scan = %+v, want succeeded with 2 history entries", repos)
	}
}

func TestWriterFailedScanKeepsDependencies(t *testing.T) {
	ctx := context.Background()
	f := Files{Dir: t.TempDir()}
	w := Writer{Backend: f, History: 10}
	if err := w.Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "1.1", "")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Repo(ctx, "homelab", nil, errkind.Wrap(errkind.Auth, errors.New("denied"))); err != nil {
		t.Fatal(err)
	}
	if deps, _ := f.ListDependencies(ctx, ""); len(deps) != 1 {
		t.Errorf("dependencies = %d, want the previous one kept", len(deps))
	}
	repos, _ := f.ListRepositoryScans(ctx)
	st := repos[0].Status
	if st.Phase != PhaseFailed || st.Error == nil || st.Error.Kind != errkind.Auth || st.LastSuccessTime == nil {
		t.Errorf("status = %+v, want failed/auth with the last success kept", st)
	}
}

func modTime(t *testing.T, f Files, dep string, deps []Dependency) int64 {
	t.Helper()
	for _, d := range deps {
		if d.Status.Dependency == dep {
			info, err := os.Stat(filepath.Join(f.Dir, "dependencies", d.Name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			return info.ModTime().UnixNano()
		}
	}
	t.Fatalf("no dependency %s", dep)
	return 0
}

// TestPruneRemovesUnconfiguredRepositories: a repository dropped from the config leaves no reports behind.
func TestPruneRemovesUnconfiguredRepositories(t *testing.T) {
	ctx := context.Background()
	f := Files{Dir: t.TempDir()}
	w := Writer{Backend: f, History: 1}
	for _, repo := range []string{"keep", "gone"} {
		r := res("a", "1.0", "1.1", "")
		r.Source = repo
		if err := w.Repo(ctx, repo, []scan.Result{r}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Prune(ctx, []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	repos, _ := f.ListRepositoryScans(ctx)
	deps, _ := f.ListDependencies(ctx, "")
	if len(repos) != 1 || repos[0].Status.Repo != "keep" || len(deps) != 1 || deps[0].Status.Source != "keep" {
		t.Errorf("after prune: repos %+v, deps %d; want only keep's", repos, len(deps))
	}
}

// TestUnreadableReportIsSkipped: one corrupt object must not hide every other report.
func TestUnreadableReportIsSkipped(t *testing.T) {
	ctx := context.Background()
	f := Files{Dir: t.TempDir()}
	if err := (Writer{Backend: f, History: 1}).Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "", "")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Dir, "dependencies", "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(f)
	p.load(ctx)
	if got := len(p.Snapshot().Deps); got != 1 {
		t.Errorf("snapshot has %d dependencies, want the readable one", got)
	}
}

// TestConcurrentWritesStayParseable: two writers of one object never publish a mixed file.
func TestConcurrentWritesStayParseable(t *testing.T) {
	f := Files{Dir: t.TempDir()}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := Dependency{ObjectMeta: metav1.ObjectMeta{Name: "same"}, Status: DepStatus{Result: scan.Result{Dependency: strings.Repeat("x", 1000*(i+1))}}}
			if err := f.PutDependency(context.Background(), &d); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if deps, err := f.ListDependencies(context.Background(), ""); err != nil || len(deps) != 1 {
		t.Errorf("after concurrent writes: %d dependencies, %v; want one parseable object", len(deps), err)
	}
}

// TestRepositoryOverTheCapFails: a repository past MaxDependencies stores nothing new and says why.
func TestRepositoryOverTheCapFails(t *testing.T) {
	ctx := context.Background()
	f := Files{Dir: t.TempDir()}
	w := Writer{Backend: f, History: 1}
	results := make([]scan.Result, MaxDependencies+1)
	for i := range results {
		results[i] = res(strconv.Itoa(i), "1.0", "", "")
	}
	if err := w.Repo(ctx, "homelab", results, nil); err != nil {
		t.Fatal(err)
	}
	repos, _ := f.ListRepositoryScans(ctx)
	deps, _ := f.ListDependencies(ctx, "")
	if len(deps) != 0 || repos[0].Status.Error == nil || repos[0].Status.Error.Kind != errkind.TooLarge {
		t.Errorf("deps %d, status %+v; want none stored and a too_large failure", len(deps), repos[0].Status)
	}
}

// TestFilesLockAllowsOneScan: a second local scan on the same data directory is refused until the first releases it.
func TestFilesLockAllowsOneScan(t *testing.T) {
	f := Files{Dir: t.TempDir()}
	unlock, err := f.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lock(); !errors.Is(err, trigger.ErrAlreadyRunning) {
		t.Fatalf("second Lock = %v, want trigger.ErrAlreadyRunning", err)
	}
	unlock()
	unlock2, err := f.Lock()
	if err != nil {
		t.Fatalf("Lock after release = %v", err)
	}
	unlock2()
}

func TestWriterLabelsTheRelease(t *testing.T) {
	ctx := context.Background()
	f := Files{Dir: t.TempDir()}
	if err := (Writer{Backend: f, History: 1, Instance: "prod"}).Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "", "")}, nil); err != nil {
		t.Fatal(err)
	}
	repos, _ := f.ListRepositoryScans(ctx)
	deps, _ := f.ListDependencies(ctx, "")
	if repos[0].Labels[LabelInstance] != "prod" || deps[0].Labels[LabelInstance] != "prod" {
		t.Errorf("labels %v / %v, want app.kubernetes.io/instance=prod on both kinds", repos[0].Labels, deps[0].Labels)
	}
}
