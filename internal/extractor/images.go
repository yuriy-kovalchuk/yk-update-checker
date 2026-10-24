package extractor

import (
	"bytes"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProtocolImage marks refs that resolve against OCI *container image* registries rather than Helm chart repositories.
const ProtocolImage = "image"

// Images extracts container image references from any Kubernetes manifest.
type Images struct{}

// NewImages builds an Images extractor.
func NewImages() *Images { return &Images{} }

// Type returns "image".
func (Images) Type() string { return "image" }

// Match reports whether the file may contain an image reference.
func (Images) Match(f *File) bool {
	return IsYAML(f.Path) && bytes.Contains(f.Content, []byte("image:"))
}

// IsYAML reports whether path names a YAML file.
func IsYAML(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// PrepareFile implements Extractor; the extractor is stateless.
func (Images) PrepareFile(*File) error { return nil }

// containerListKeys are the PodSpec fields that hold containers.
var containerListKeys = map[string]bool{
	"containers":          true,
	"initContainers":      true,
	"ephemeralContainers": true,
}

// Extract returns one ref per distinct image per workload document.
func (Images) Extract(f *File) (string, []Ref, error) {
	var refs []Ref
	seen := map[string]bool{}

	for _, root := range f.Documents() {
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}
		kind := mappingScalar(root, "kind")
		if !isWorkloadKind(kind) {
			continue
		}
		workload := kind
		if name := mappingScalar(mappingValue(root, "metadata"), "name"); name != "" {
			workload = kind + "/" + name
		}

		var docRefs []Ref
		forEachImage(root, func(image string) {
			ref, ok := parseImageRef(image)
			if !ok {
				return
			}
			key := workload + "|" + ref.Repository + "|" + ref.Tag + "|" + ref.Digest
			if seen[key] {
				return
			}
			seen[key] = true
			docRefs = append(docRefs, Ref{
				Chart:          workload,
				Name:           ref.Name,
				Protocol:       ProtocolImage,
				Repository:     ref.Repository,
				CurrentVersion: ref.Tag,
				Digest:         ref.Digest,
				Kind:           kind,
			})
		})
		refs = append(refs, docRefs...)
	}
	return "", refs, nil
}

// isWorkloadKind rejects documents whose "image:" entries are schema definitions rather than containers.
func isWorkloadKind(kind string) bool {
	switch kind {
	case "", "CustomResourceDefinition", "APIVersion":
		return false
	default:
		return true
	}
}

// forEachImage calls fn with every container image string found under n.
func forEachImage(n *yaml.Node, fn func(image string)) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i].Value, n.Content[i+1]
			if containerListKeys[key] && val.Kind == yaml.SequenceNode {
				for _, c := range val.Content {
					if c.Kind != yaml.MappingNode {
						continue
					}
					if img := mappingValue(c, "image"); img != nil && img.Kind == yaml.ScalarNode && img.Value != "" {
						fn(img.Value)
					}
				}
			}
			forEachImage(val, fn)
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			forEachImage(c, fn)
		}
	case yaml.AliasNode:
		forEachImage(n.Alias, fn)
	case yaml.ScalarNode:
		// a leaf: no containers below it
	}
}

// mappingValue returns the value node for key in a mapping node.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mappingScalar returns the string value of key, or "".
func mappingScalar(m *yaml.Node, key string) string {
	if v := mappingValue(m, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

// imageRef is a parsed container image reference.
type imageRef struct {
	Name       string // as written, e.g. "alpine" or "ghcr.io/foo/bar"
	Repository string // canonical path for registry lookups, e.g. "index.docker.io/library/alpine"
	Tag        string
	Digest     string
}

// parseImageRef splits "repository[:tag][@digest]".
func parseImageRef(s string) (imageRef, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "${") || strings.Contains(s, "{{") || strings.Contains(s, "$(") {
		return imageRef{}, false
	}
	var ref imageRef
	if i := strings.Index(s, "@"); i >= 0 {
		ref.Digest = s[i+1:]
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[i:], "/") {
		ref.Tag = s[i+1:]
		s = s[:i]
	}
	if s == "" {
		return imageRef{}, false
	}
	ref.Name = s
	ref.Repository = canonicalRepository(s)
	return ref, true
}

// canonicalRepository qualifies Docker Hub short names with their registry host, matching the resolution rules the Docker client uses.
func canonicalRepository(s string) string {
	first, _, _ := strings.Cut(s, "/")
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return s
	}
	if strings.Contains(s, "/") {
		return "index.docker.io/" + s
	}
	return "index.docker.io/library/" + s
}
