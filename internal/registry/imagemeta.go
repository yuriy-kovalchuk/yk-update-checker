package registry

import (
	"context"
	"slices"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// imageMeta reads manifest annotations and the image config blob. Both are small JSON documents; no layer is ever downloaded.
func (c *IndexCache) imageMeta(ctx context.Context, repository, tag, digest string) (imageMeta, error) {
	spec := repository + ":" + tag
	if digest != "" {
		spec = repository + "@" + digest
	}
	return getOrDo(ctx, c, "image-meta", c.imgMetas, spec, func(ctx context.Context) (imageMeta, error) {
		return c.fetchImageMeta(ctx, spec)
	})
}

// fetchImageMeta reads manifest annotations and the config blob for one image.
func (c *IndexCache) fetchImageMeta(ctx context.Context, spec string) (imageMeta, error) {
	ref, err := name.ParseReference(spec, name.WeakValidation)
	if err != nil {
		return imageMeta{}, err
	}
	desc, err := remote.Get(ref, c.remoteOptions(ctx)...)
	if err != nil {
		return imageMeta{}, err
	}
	meta := imageMeta{Digest: desc.Digest.String()}
	mergeAnnotations(&meta, desc.Annotations)
	if desc.MediaType.IsIndex() {
		meta.IsIndex = true
		addIndexChildren(&meta, desc)
	}
	addConfig(&meta, desc)
	return meta, nil
}

// addIndexChildren records the per-platform manifests a multi-arch index lists.
func addIndexChildren(meta *imageMeta, desc *remote.Descriptor) {
	idx, err := desc.ImageIndex()
	if err != nil {
		return
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return
	}
	for _, child := range im.Manifests {
		meta.Children = append(meta.Children, child.Digest.String())
		if child.Platform != nil {
			meta.addPlatform(*child.Platform)
		}
	}
}

// addConfig merges what the image config blob says: labels, the build date and, for a single-platform image, its platform.
func addConfig(meta *imageMeta, desc *remote.Descriptor) {
	img, err := desc.Image()
	if err != nil {
		return
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return
	}
	mergeAnnotations(meta, cf.Config.Labels)
	if created := cf.Created.Time; created.Unix() > 0 {
		meta.Created, meta.HasCreated = created, true
	}
	if p := cf.Platform(); p != nil && !meta.IsIndex {
		meta.addPlatform(*p)
	}
}

// OCI image annotations/labels that describe the source version.
const (
	labelVersion  = "org.opencontainers.image.version"
	labelRevision = "org.opencontainers.image.revision"
	labelCreated  = "org.opencontainers.image.created"
)

func mergeAnnotations(meta *imageMeta, kv map[string]string) {
	if len(kv) == 0 {
		return
	}
	if meta.Version == "" {
		meta.Version = kv[labelVersion]
	}
	if meta.Revision == "" {
		meta.Revision = kv[labelRevision]
	}
	if !meta.HasCreated {
		if s := kv[labelCreated]; s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil && t.Unix() > 0 {
				meta.Created, meta.HasCreated = t, true
			}
		}
	}
}

// imageMeta is the small amount of registry metadata the comparison needs.
type imageMeta struct {
	Digest     string
	Version    string
	Revision   string
	Created    time.Time
	HasCreated bool
	// IsIndex is true when the tag serves a manifest index (a multi-arch image) instead of a single image manifest; Platforms lists what that index serves.
	IsIndex   bool
	Platforms []string
	// Children are the digests of the per-platform manifests an index lists.
	Children []string
}

// serves reports whether pulling this tag can yield digest, its own or a platform manifest.
func (m imageMeta) serves(digest string) bool {
	return digest == m.Digest || slices.Contains(m.Children, digest)
}

// addPlatform records an "os/arch" entry once.
func (m *imageMeta) addPlatform(p v1.Platform) {
	if p.OS == "" || p.Architecture == "" {
		return
	}
	entry := p.OS + "/" + p.Architecture
	for _, have := range m.Platforms {
		if have == entry {
			return
		}
	}
	m.Platforms = append(m.Platforms, entry)
}

// multiArch reports whether a tag is known to serve more than one platform.
func (m imageMeta) multiArch() bool { return m.IsIndex && len(m.Platforms) > 1 }
