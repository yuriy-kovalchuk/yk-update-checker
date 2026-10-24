package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ImageInfo is the outcome of checking one container image reference.
type ImageInfo struct {
	// Latest is the newest tag comparable with the current one; empty means either "already current" or "nothing comparable found" (see Note).
	Latest string
	// Kind classifies the tag that was checked.
	Kind TagKind
	// Resolved is the version a floating tag actually points at today, read from OCI metadata (org.opencontainers.image.version).
	Resolved string
	// Digest is what the checked tag points at right now.
	Digest string
	// Note carries caveats the UI should show (unusable dates, partial listings, rejected candidate tags).
	Note string
	// Skipped lists tags that rank above the current one by version but were rejected as stray; empty when nothing was rejected.
	Skipped []SkippedTag
	// CurrentPushed and LatestPushed are the last push dates the vendor API reported during verification; zero when unknown.
	CurrentPushed, LatestPushed time.Time
}

// LatestImage reports the newest tag for an image reference that is comparable with curTag under the given scope.
func (c *IndexCache) LatestImage(ctx context.Context, repository, curTag, pinnedDigest string, scope Scope) (ImageInfo, error) {
	info, err := getOrDo(ctx, c, "image-check", c.imgChecks, imageKey(repository, curTag, pinnedDigest, scope),
		func(ctx context.Context) (ImageInfo, error) {
			return c.lookupImage(ctx, repository, curTag, pinnedDigest, scope)
		})
	return info, classify(err)
}

func imageKey(repository, curTag, digest string, scope Scope) string {
	return "image://" + repository + "|" + curTag + "|" + digest + "|" + string(scope)
}

func (c *IndexCache) lookupImage(ctx context.Context, repository, curTag, pinnedDigest string, scope Scope) (ImageInfo, error) {
	cur := classifyTag(curTag)
	info := ImageInfo{Kind: cur.Kind}

	// Floating or version-less tags need registry metadata to say anything at all.
	base := cur
	if curTag == "" || !cur.comparable() || pinnedDigest != "" {
		var err error
		if base, err = c.resolveMeta(ctx, repository, curTag, pinnedDigest, &info); err != nil {
			return ImageInfo{}, err
		}
	}
	if pinnedDigest != "" && curTag != "" {
		c.tagDigest(ctx, repository, curTag, pinnedDigest, &info)
	}
	if base.comparable() {
		return c.compareVersions(ctx, repository, curTag, base, scope, info)
	}

	// No version in the tag: fall back to build/push dates of same-family tags.
	dated := c.newestDatedTag(ctx, repository, curTag)
	info.Latest = dated.Tag
	info.Note = joinNotes(info.Note, dated.Note)
	return info, nil
}

// resolveMeta reads a floating or pinned reference's metadata and returns the version shape to compare from.
func (c *IndexCache) resolveMeta(ctx context.Context, repository, curTag, pinnedDigest string, info *ImageInfo) (tagShape, error) {
	base := classifyTag(curTag)
	metaRef := curTag
	if metaRef == "" {
		metaRef = "latest"
	}
	meta, err := c.imageMeta(ctx, repository, metaRef, pinnedDigest)
	if err != nil {
		if pinnedDigest == "" {
			return base, err
		}
		return base, nil
	}
	info.Digest = meta.Digest
	resolved := classifyTag(meta.Version)
	if !resolved.comparable() {
		return base, nil
	}
	// The version label is inherited from the base image unless the packager sets it, so "Ubuntu 24.04" can show up as the image's own version.
	if _, err := c.imageMeta(ctx, repository, meta.Version, ""); err != nil {
		info.Note = fmt.Sprintf("metadata claims version %s, which this repository has no tag for; ignoring", meta.Version)
		return base, nil
	}
	info.Resolved = meta.Version
	info.Kind = resolved.Kind
	return resolved, nil
}

// tagDigest reports what a pinned reference's tag serves today.
func (c *IndexCache) tagDigest(ctx context.Context, repository, curTag, pinnedDigest string, info *ImageInfo) {
	tagMeta, err := c.imageMeta(ctx, repository, curTag, "")
	if err != nil {
		info.Note = joinNotes(info.Note, fmt.Sprintf("tag %s could not be resolved, so whether it still serves the pinned digest is unknown", curTag))
		return
	}
	if tagMeta.serves(pinnedDigest) {
		info.Digest = pinnedDigest
	} else {
		info.Digest = tagMeta.Digest
	}
}

// compareVersions finds the newest tag that outranks base within scope, verifying the winners unless verification is off.
func (c *IndexCache) compareVersions(ctx context.Context, repository, curTag string, base tagShape, scope Scope, info ImageInfo) (ImageInfo, error) {
	tags, exhaustive, ferr := c.imageTags(ctx, repository, searchAnchors(curTag, base.Raw)...)
	if len(tags) == 0 && ferr != nil {
		return ImageInfo{}, ferr // registry unreachable: report it, don't imply "up to date"
	}
	ranked := rankedCandidates(base, tags, scope)
	if len(ranked) == 0 {
		if info.Resolved != "" {
			info.Note = joinNotes(info.Note, fmt.Sprintf("tag %s currently serves %s", curTag, info.Resolved))
		}
		info.Note = joinNotes(info.Note, exhaustedNote(exhaustive, len(tags)))
		return info, nil
	}
	if !c.verifyTags {
		info.Latest = ranked[0].Raw
		info.Note = windowedNote(exhaustive, len(tags))
		return info, nil
	}
	best, skipped, vnote := c.verifyCandidates(ctx, repository, base, ranked, tags)
	info.Latest = best
	info.Skipped = skipped
	info.CurrentPushed, info.LatestPushed = c.pushDates(ctx, repository, base.Raw, best)
	info.Note = joinNotes(windowedNote(exhaustive, len(tags)), vnote)
	return info, nil
}

// pushDates looks up the last push of the current and the latest tag.
func (c *IndexCache) pushDates(ctx context.Context, repository, cur, latest string) (curAt, latestAt time.Time) {
	want := []string{cur}
	if latest != "" {
		want = append(want, latest)
	}
	dates, ok := c.bulkTagDates(ctx, repository, want...)
	if !ok {
		return
	}
	return dates.infos[cur].Pushed, dates.infos[latest].Pushed
}

// joinNotes merges note fragments without duplicating text.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case strings.Contains(a, b):
		return a
	default:
		return a + "; " + b
	}
}

// windowedNote flags that the winning tag came from a partial listing.
func windowedNote(exhaustive bool, n int) string {
	if exhaustive {
		return ""
	}
	return fmt.Sprintf("compared against %d tags listed around the current one", n)
}

// exhaustedNote states what the bounded search did and did not cover.
func exhaustedNote(exhaustive bool, n int) string {
	if exhaustive {
		return "no newer comparable tag found"
	}
	return fmt.Sprintf("no newer comparable tag among %d tags listed around the current one; "+
		"tag listings are capped, so an older listing position can hide a newer version", n)
}

// datedTag is a candidate selected by build date rather than by version.
type datedTag struct {
	Tag  string
	Note string
}

// newestDatedTag compares build dates when tags carry no version.
func (c *IndexCache) newestDatedTag(ctx context.Context, repository, curTag string) datedTag {
	if dates, ok := c.bulkTagDates(ctx, repository, curTag); ok {
		cur, ok := dates.infos[curTag]
		if !ok {
			return datedTag{Note: "current tag not found in registry tag listing"}
		}
		var best string
		var bestTime time.Time
		for tag, info := range dates.infos {
			if !variantPeer(curTag, tag) || !info.Pushed.After(cur.Pushed) {
				continue
			}
			if info.Pushed.After(bestTime) {
				best, bestTime = tag, info.Pushed
			}
		}
		if best == "" {
			return datedTag{Note: fmt.Sprintf("no tag pushed after %s in the %d most recent tags", cur.Pushed.Format(time.RFC3339), len(dates.infos))}
		}
		return datedTag{Tag: best, Note: fmt.Sprintf("selected by push date: %s pushed after %s", best, curTag)}
	}

	tags, _, _ := c.imageTags(ctx, repository, curTag)
	if len(tags) == 0 {
		return datedTag{Note: "no tags found for this image"}
	}
	family := filterFamily(tags, curTag)
	if len(family) == 0 {
		return datedTag{Note: fmt.Sprintf("no other tag matches the build variant of %s", curTag)}
	}
	// Listings run oldest-first, so keep the tail: those are the newest builds.
	if len(family) > c.limits.dateCandidates {
		family = family[len(family)-c.limits.dateCandidates:]
	}
	meta, err := c.imageMeta(ctx, repository, curTag, "")
	if err != nil {
		return datedTag{Note: "current image metadata unavailable"}
	}
	if !meta.HasCreated {
		return datedTag{Note: "current image exposes no usable build date"}
	}
	var best string
	var bestTime time.Time
	for _, tag := range family {
		m, err := c.imageMeta(ctx, repository, tag, "")
		if err != nil || !m.HasCreated {
			continue
		}
		if m.Created.After(meta.Created) && m.Created.After(bestTime) {
			best, bestTime = tag, m.Created
		}
	}
	if best == "" {
		return datedTag{Note: fmt.Sprintf("no newer build date among %d candidate tags", len(family))}
	}
	return datedTag{Tag: best, Note: fmt.Sprintf("selected by build date: %s built %s", best, bestTime.Format("2006-01-02"))}
}

func filterFamily(tags []string, curTag string) []string {
	prefix := shapePrefix(classifyTag(curTag))
	if shaLike(prefix) {
		prefix = "" // a commit hash is not a family; the variant segments are
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if variantPeer(curTag, t) && strings.HasPrefix(t, prefix) {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// variantPeer reports whether tag is another concrete build of curTag's variant.
func variantPeer(curTag, tag string) bool {
	if tag == curTag || floatingTags[strings.ToLower(tag)] {
		return false
	}
	return floatingTags[strings.ToLower(curTag)] || sameVariant(curTag, tag)
}
