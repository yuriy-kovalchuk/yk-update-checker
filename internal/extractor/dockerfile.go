package extractor

import (
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Dockerfile extracts the base images named by FROM instructions.
type Dockerfile struct{}

// NewDockerfile creates a new Dockerfile extractor.
func NewDockerfile() *Dockerfile { return &Dockerfile{} }

// Type returns "dockerfile".
func (Dockerfile) Type() string { return "dockerfile" }

// Match reports whether the file is a Dockerfile or Containerfile.
func (Dockerfile) Match(f *File) bool { return IsDockerfile(f.Path) }

// PrepareFile implements Extractor; the extractor is stateless.
func (Dockerfile) PrepareFile(*File) error { return nil }

// dockerfileNames are the build-definition file names Docker and Podman accept, with prefixes or suffixes.
var dockerfileNames = []string{"dockerfile", "containerfile"}

// IsDockerfile reports whether path names a container build definition.
func IsDockerfile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	for _, name := range dockerfileNames {
		if base == name || strings.HasPrefix(base, name+".") || strings.HasSuffix(base, "."+name) {
			return true
		}
	}
	return false
}

// fromLine matches a FROM instruction once continuation lines are joined and comments are gone.
var fromLine = regexp.MustCompile(`(?i)^\s*from\s+(.*)$`)

// buildStage is one FROM instruction of a build.
type buildStage struct {
	image          string // as written, empty when unresolvable (build arg)
	alias          string // "AS <alias>", empty when the stage is unnamed
	aliasOfEarlier bool   // FROM names a stage of this same build
}

// Extract returns one ref per FROM that names a real base image, each labelled "<file>/<stage>" and the last one flagged as the final stage.
func (Dockerfile) Extract(f *File) (string, []Ref, error) {
	path := f.Path
	stages := parseFroms(string(f.Content))
	if len(stages) == 0 {
		return "", nil, nil
	}

	// The final stage is the last one that ships an image to check: a stage starting FROM an earlier stage of the same build has nothing to compare.
	final := -1
	for i, st := range stages {
		if checkable(st) {
			final = i
		}
	}

	base := filepath.Base(path)
	var refs []Ref
	for i, st := range stages {
		if !checkable(st) {
			continue
		}
		ref, ok := parseImageRef(st.image)
		if !ok {
			continue
		}
		stage := st.alias
		if stage == "" {
			stage = "stage" + strconv.Itoa(i+1)
		}
		refs = append(refs, Ref{
			Chart:          base + "/" + stage,
			Name:           ref.Name,
			Protocol:       ProtocolImage,
			Repository:     ref.Repository,
			CurrentVersion: ref.Tag,
			Digest:         ref.Digest,
			FinalStage:     i == final,
		})
	}
	return "", refs, nil
}

// checkable reports whether a stage's base image is something a registry can answer questions about.
func checkable(st buildStage) bool {
	return st.image != "" && st.image != "scratch" && !st.aliasOfEarlier
}

// parseFroms returns one entry per FROM instruction, in file order.
func parseFroms(content string) []buildStage {
	var stages []buildStage
	aliasOf := map[string]bool{}

	for _, raw := range splitLogicalLines(content) {
		m := fromLine.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		fields := strings.Fields(m[1])
		var image, alias string
		for i := 0; i < len(fields); i++ {
			f := fields[i]
			switch {
			case strings.HasPrefix(f, "--"):
				// --platform=…, --keep-git-dir=…: flags, not the image.
			case strings.EqualFold(f, "as") && i+1 < len(fields):
				alias = fields[i+1]
				i++
			case image == "":
				image = f
			}
		}
		st := buildStage{image: image, alias: alias}
		// Checked before this stage's own alias is recorded: a stage cannot be its own base image.
		if image != "" && aliasOf[strings.ToLower(image)] {
			st.aliasOfEarlier = true
		}
		if alias != "" {
			aliasOf[strings.ToLower(alias)] = true
		}
		if strings.Contains(image, "$") {
			// Build arg or variable: the pin is not a registry reference.
			slog.Debug("skipping build-arg base image", "image", image)
			st.image = ""
		}
		stages = append(stages, st)
	}
	return stages
}

// splitLogicalLines drops comments and CRs, and joins backslash-continued lines into one string per instruction.
func splitLogicalLines(content string) []string {
	var out []string
	var cur string
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, `\`) {
			cur += strings.TrimSuffix(trimmed, `\`) + " "
			continue
		}
		out = append(out, cur+trimmed)
		cur = ""
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
