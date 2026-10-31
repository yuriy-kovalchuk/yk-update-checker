package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// SkippedTag is a tag that outranks the current one by version but was rejected as a stray rather than a release.
type SkippedTag struct {
	// +kubebuilder:validation:MaxLength=1024
	Tag string `json:"tag"`
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MaxLength=4096
	Reasons []string `json:"reasons"`
}

// verifyCandidates walks the ranked candidates and returns the first that survives verification, the candidates that were rejected and a note about both.
func (c *IndexCache) verifyCandidates(ctx context.Context, repository string, cur tagShape, ranked []tagShape, tags []string) (string, []SkippedTag, string) {
	cands := ranked
	if len(cands) > c.limits.verifyCandidates {
		cands = cands[:c.limits.verifyCandidates]
	}
	wanted := make([]string, 0, len(cands)+1)
	wanted = append(wanted, cur.Raw)
	for _, cand := range cands {
		wanted = append(wanted, cand.Raw)
	}

	bl := c.newVerifyBaseline(ctx, repository, cur, wanted)

	var skipped []SkippedTag
	for _, cand := range cands {
		sig := c.candidateSignals(ctx, bl, cand, tags)
		if len(sig.hard) > 0 || len(sig.soft) >= 2 {
			reasons := append(append([]string{}, sig.hard...), sig.soft...)
			skipped = append(skipped, SkippedTag{Tag: cand.Raw, Reasons: reasons})
			continue
		}
		note := verifyNote(skipped)
		if len(sig.soft) > 0 {
			note = joinNotes(note, fmt.Sprintf("%s kept despite: %s", cand.Raw, strings.Join(sig.soft, "; ")))
		}
		if len(sig.info) > 0 {
			note = joinNotes(note, strings.Join(sig.info, "; "))
		}
		return cand.Raw, skipped, note
	}
	note := verifyNote(skipped)
	if rest := len(ranked) - len(cands); rest > 0 {
		note = joinNotes(note, fmt.Sprintf("stopped after verifying %d candidates; %d more tags rank above %s and were not checked",
			len(cands), rest, cur.Raw))
	}
	return "", skipped, note
}

// verifyNote states what was rejected and why, e.g. "skipped v17.42.37: pushed 2021-02-18, before v1.9.1; no sibling tags".
func verifyNote(skipped []SkippedTag) string {
	parts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		parts = append(parts, fmt.Sprintf("skipped %s: %s", s.Tag, strings.Join(s.Reasons, "; ")))
	}
	return strings.Join(parts, "; ")
}

// verifyBaseline is what the current tag is known to look like.
type verifyBaseline struct {
	repository string
	cur        tagShape
	meta       imageMeta
	metaOK     bool
	dates      tagDates
	info       tagInfo
	infoOK     bool
	signed     *bool // nil: the registry says nothing about signatures
}

func (c *IndexCache) newVerifyBaseline(ctx context.Context, repository string, cur tagShape, wanted []string) *verifyBaseline {
	bl := &verifyBaseline{repository: repository, cur: cur, dates: tagDates{infos: map[string]tagInfo{}}}
	if dates, ok := c.bulkTagDates(ctx, repository, wanted...); ok {
		bl.dates = dates
	}
	bl.info, bl.infoOK = bl.dates.infos[cur.Raw]
	if meta, err := c.imageMeta(ctx, repository, cur.Raw, ""); err == nil {
		bl.meta, bl.metaOK = meta, true
	}
	if bl.metaOK {
		if signed, ok := c.hasSignatures(ctx, repository, bl.meta.Digest); ok {
			bl.signed = &signed
		}
	}
	return bl
}

// vendorInfo reports what the vendor tag API said about a tag in this look-up.
func (b *verifyBaseline) vendorInfo(tag string) (tagInfo, bool) {
	info, ok := b.dates.infos[tag]
	return info, ok && (info.HasDate || info.Digest != "")
}

// aliases lists other tags the vendor API reports as sharing a digest.
func (b *verifyBaseline) aliases(digest, except string) []string {
	if digest == "" {
		return nil
	}
	var out []string
	for tag, info := range b.dates.infos {
		if tag == except || info.Digest == "" || info.Digest != digest {
			continue
		}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// candidateSignals holds the reasons found for one candidate tag.
type candidateSignals struct {
	hard []string
	soft []string
	info []string
}

func (c *IndexCache) candidateSignals(ctx context.Context, bl *verifyBaseline, cand tagShape, tags []string) candidateSignals {
	var s candidateSignals

	// 1. Push date.
	if info, ok := bl.vendorInfo(cand.Raw); ok && info.HasDate && bl.infoOK && bl.info.HasDate &&
		bl.info.Pushed.Sub(info.Pushed) > c.limits.pushGapSignal {
		s.soft = append(s.soft, fmt.Sprintf("pushed %s, before %s pushed %s",
			info.Pushed.Format("2006-01-02"), bl.cur.Raw, bl.info.Pushed.Format("2006-01-02")))
	}

	// Manifest plus config blob: shared by the label, shape, alias and (through the digest) signature signals.
	meta, err := c.imageMeta(ctx, bl.repository, cand.Raw, "")
	metaOK := err == nil

	// 2. The OCI version label has to name the candidate.
	if metaOK {
		label := strings.TrimPrefix(meta.Version, "v")
		if label != "" && label != strings.TrimPrefix(cand.Raw, "v") &&
			(label == strings.TrimPrefix(bl.cur.Raw, "v") || slices.Contains(tags, label)) {
			s.hard = append(s.hard, fmt.Sprintf("image metadata claims version %s", meta.Version))
		}
	}

	// 3. A real release has siblings in its series: other patches, pre-releases or arch-suffixed builds.
	sibs := siblingsOf(cand, tags)
	if len(sibs) == 0 {
		sibs = siblingsOf(cand, c.siblingPage(ctx, bl.repository, cand))
	}
	if len(sibs) == 0 {
		s.soft = append(s.soft, "no sibling tags")
	}

	// 4. A candidate that jumps several majors with nothing listed in between moved too far to be a release.
	if from, to, jumped := majorJump(bl.cur, cand, tags); jumped {
		s.soft = append(s.soft, fmt.Sprintf("skips v%d..v%d, none of them listed", from, to))
	}

	// 5. Manifest shape: a single-platform candidate where the current tag is multi-arch is the signature of an ad-hoc build.
	if metaOK && bl.meta.multiArch() {
		switch {
		case !meta.IsIndex:
			s.soft = append(s.soft, fmt.Sprintf("single-arch, %s ships %s",
				bl.cur.Raw, strings.Join(bl.meta.Platforms, ",")))
		case strictSubset(bl.meta.Platforms, meta.Platforms):
			s.soft = append(s.soft, fmt.Sprintf("covers only %s of the %d platforms %s ships",
				strings.Join(meta.Platforms, ","), len(bl.meta.Platforms), bl.cur.Raw))
		}
	}

	// 6. The same digest as another tag makes this tag an alias, not a build.
	var strays []string
	for _, other := range bl.aliases(meta.Digest, cand.Raw) {
		s.info = append(s.info, "alias of "+other)
		shape := classifyTag(other)
		// The alias spells the candidate's own version as a pre-release, so the content is that build whatever the tag claims.
		if ownPreRelease(shape, cand) {
			s.hard = append(s.hard, "image is also tagged "+other)
			continue
		}
		// An alias of a floating tag (latest, nightly) is what a current release looks like too, so on its own it says nothing.
		if shape.Kind == TagDate || shape.Kind == TagOpaque || isPrerelease(shape) {
			strays = append(strays, other)
		}
	}
	// One build published under several names is one piece of evidence, not one per name.
	if len(strays) > 0 {
		s.soft = append(s.soft, "same digest as "+strings.Join(strays, ", "))
	}

	// 7. Unsigned where the current tag is signed. If the current tag carries no signatures there is nothing to compare against.
	if metaOK && bl.signed != nil && *bl.signed {
		if signed, ok := c.hasSignatures(ctx, bl.repository, meta.Digest); ok && !signed {
			s.soft = append(s.soft, "unsigned, while "+bl.cur.Raw+" is signed")
		}
	}
	return s
}

// ownPreRelease reports whether other names the candidate's own version as a pre-release: 9.2.0-rc1 beside 9.2.
func ownPreRelease(other, cand tagShape) bool {
	if !isPrerelease(other) || !other.comparable() || len(other.Core) < len(cand.Core) {
		return false
	}
	for i, g := range cand.Core {
		if group(other.Core, i) != g {
			return false
		}
	}
	return true
}

// strictSubset reports whether every entry of sub is in super while sub holds fewer of them.
func strictSubset(super, sub []string) bool {
	if len(sub) == 0 || len(sub) >= len(super) {
		return false
	}
	for _, s := range sub {
		if !slices.Contains(super, s) {
			return false
		}
	}
	return true
}

// siblingsOf returns the tags that belong to the candidate's series: the same literal family (identical prefix and kind) at the same major version.
func siblingsOf(cand tagShape, tags []string) []string {
	var out []string
	for _, raw := range tags {
		if raw == cand.Raw {
			continue
		}
		if strings.HasPrefix(raw, cand.Raw+"-") {
			out = append(out, raw) // arch-suffixed build of this very version
			continue
		}
		t := classifyTag(raw)
		if sameSeries(t, cand) && (len(cand.Core) == 1 || group(t.Core, 0) == group(cand.Core, 0)) {
			out = append(out, raw)
		}
	}
	return out
}

// sameSeries reports whether t is a versioned tag of cand's literal family.
func sameSeries(t, cand tagShape) bool {
	return t.Kind == cand.Kind && t.Pre == cand.Pre && t.comparable()
}

// siblingProbe is the lexicographic cursor that lands inside the candidate's series: "v17" for v17.42.37, "server-vulkan-b" for a build counter.
func siblingProbe(cand tagShape) string {
	if !cand.comparable() {
		return ""
	}
	if len(cand.Core) == 1 {
		return cand.Pre
	}
	return cand.Pre + strconv.Itoa(cand.Core[0])
}

// siblingPage fetches one extra page of the listing, anchored in the candidate's series, for the sibling check.
func (c *IndexCache) siblingPage(ctx context.Context, repository string, cand tagShape) []string {
	probe := siblingProbe(cand)
	if probe == "" {
		return nil
	}
	repo, client, err := c.registryClient(ctx, repository)
	if err != nil {
		return nil
	}
	page, _, _ := c.tagsPage(ctx, client, repo, probe, "")
	return page
}

// majorJump reports a candidate that lands more than one major ahead of the current tag while the listing holds nothing for the majors in between.
func majorJump(cur, cand tagShape, tags []string) (int, int, bool) {
	if cur.Kind != TagSemver || len(cur.Core) < 2 || len(cand.Core) < 2 {
		return 0, 0, false
	}
	from, to := group(cur.Core, 0)+1, group(cand.Core, 0)-1
	if from > to {
		return 0, 0, false
	}
	for _, raw := range tags {
		if t := classifyTag(raw); sameSeries(t, cand) && group(t.Core, 0) >= from && group(t.Core, 0) <= to {
			return 0, 0, false
		}
	}
	return from, to, true
}

// sigStatus records what the registry said about one digest: whether anything points at it as a signature, and whether it answered the question at all.
type sigStatus struct {
	signed   bool
	answered bool
}

// hasSignatures reports whether cosign signatures or OCI referrers are attached to an image digest.
func (c *IndexCache) hasSignatures(ctx context.Context, repository, digest string) (bool, bool) {
	if digest == "" {
		return false, false
	}
	st, err := getOrDo(ctx, c, "signatures", c.imgSigs, repository+"|signatures|"+digest, func(ctx context.Context) (sigStatus, error) {
		return c.signatureStatus(ctx, repository, digest)
	})
	if err != nil {
		return false, false
	}
	return st.signed, st.answered
}

// signatureStatus probes cosign's .sig tag and the OCI referrers endpoint; two HEADs, never a body.
func (c *IndexCache) signatureStatus(ctx context.Context, repository, digest string) (sigStatus, error) {
	stored := strings.ReplaceAll(digest, ":", "-")
	for _, suffix := range []string{".sig", ".att"} {
		ok, err := c.manifestExists(ctx, repository, stored+suffix)
		if err != nil {
			return sigStatus{}, err
		}
		if ok {
			return sigStatus{signed: true, answered: true}, nil
		}
	}
	if n, err := c.referrerCount(ctx, repository, digest); err == nil {
		return sigStatus{signed: n > 0, answered: true}, nil
	}
	// The manifest endpoint answered ("no such tag") and there is no referrers endpoint, so this image really carries nothing.
	return sigStatus{answered: true}, nil
}

// manifestExists HEADs one manifest reference without downloading it.
func (c *IndexCache) manifestExists(ctx context.Context, repository, ref string) (bool, error) {
	repo, client, err := c.registryClient(ctx, repository)
	if err != nil {
		return false, err
	}
	u := registryURL(repo, "manifests/"+ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, httpStatusError(u, resp.StatusCode)
	}
}

// referrerCount asks the OCI referrers endpoint how many artifacts point at a digest.
func (c *IndexCache) referrerCount(ctx context.Context, repository, digest string) (int, error) {
	return getOrDo(ctx, c, "referrers", c.imgRefs, repository+"|"+digest, func(ctx context.Context) (int, error) {
		repo, client, err := c.registryClient(ctx, repository)
		if err != nil {
			return 0, err
		}
		var body struct {
			Manifests []json.RawMessage `json:"manifests"`
		}
		_, err = c.getJSON(ctx, client, registryURL(repo, "referrers/"+digest), "application/vnd.oci.image.index.v1+json", &body)
		return len(body.Manifests), err
	})
}
