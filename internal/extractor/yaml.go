package extractor

import (
	"bytes"
	"errors"
	"io"
	"log/slog"

	"gopkg.in/yaml.v3"
)

// decodeDocuments returns the root node of every document in a YAML file.
func decodeDocuments(content []byte) []*yaml.Node {
	roots, complete := decodeStream(content)
	if complete {
		return roots
	}
	roots = nil
	for _, chunk := range splitYAMLDocuments(content) {
		if root, ok := decodeDocument(chunk); ok {
			roots = append(roots, root)
		}
	}
	return roots
}

// decodeAll decodes every document of a YAML file once; documents that do not fit T or fail to parse are skipped.
func decodeAll[T any](f *File) []T {
	var out []T
	for _, root := range f.Documents() {
		var v T
		if err := root.Decode(&v); err != nil {
			slog.Debug("skipping YAML document", "error", err)
			continue
		}
		out = append(out, v)
	}
	return out
}

// decodeStream decodes a whole YAML stream, reporting whether it reached EOF.
func decodeStream(content []byte) ([]*yaml.Node, bool) {
	var roots []*yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return roots, true
			}
			return roots, false
		}
		if root := documentRoot(&doc); root != nil {
			roots = append(roots, root)
		}
	}
}

// decodeDocument parses a single YAML document.
func decodeDocument(content []byte) (*yaml.Node, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, false
	}
	root := documentRoot(&doc)
	return root, root != nil
}

// splitYAMLDocuments splits content on "---"/"..." document boundaries.
func splitYAMLDocuments(content []byte) [][]byte {
	var chunks [][]byte
	var cur []byte
	flush := func() {
		if len(bytes.TrimSpace(cur)) > 0 {
			chunks = append(chunks, cur)
		}
		cur = nil
	}
	for _, line := range bytes.Split(content, []byte("\n")) {
		switch t := bytes.TrimSpace(line); string(t) {
		case "---", "...":
			flush()
		default:
			cur = append(cur, line...)
			cur = append(cur, '\n')
		}
	}
	flush()
	return chunks
}

// documentRoot unwraps a decoded document node.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		return doc.Content[0]
	}
	return doc
}
