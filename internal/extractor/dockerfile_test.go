package extractor

import (
	"strings"
	"testing"
)

func TestIsDockerfile(t *testing.T) {
	yes := []string{
		"Dockerfile", "dockerfile", "DOCKERFILE",
		"Dockerfile.dev", "dockerfile.prod",
		"api.Dockerfile", "worker.dockerfile",
		"Containerfile", "containerfile", "Containerfile.debug", "app.Containerfile",
		"build/Dockerfile", "build/api.Dockerfile",
		// Any "Dockerfile.<suffix>" spelling is a build definition variant.
		"Dockerfile.md", "chart/Dockerfile.helm",
	}
	no := []string{
		"docker-compose.yml", "compose.yaml", ".dockerignore", "README.md",
		"values.yaml", "images/docker.dockerfilex",
	}
	for _, path := range yes {
		if !IsDockerfile(path) {
			t.Errorf("IsDockerfile(%q) = false, want true", path)
		}
	}
	for _, path := range no {
		if IsDockerfile(path) {
			t.Errorf("IsDockerfile(%q) = true, want false", path)
		}
	}
}

const multiStage = `# syntax=docker/dockerfile:1
# a comment mentioning FROM nope:3

ARG BUILDPLATFORM

From --platform=$BUILDPLATFORM golang:1.24 AS build
RUN CGO_ENABLED=0 go build -o /out/app ./...

from scratch AS ignored

FROM build AS bundled
COPY --from=build /out/app /app

FROM alpine:3.20 \
     AS runtime
RUN adduser -D app
USER app
ENTRYPOINT ["/app"]
`

func TestDockerfileExtractMultiStage(t *testing.T) {
	_, refs, err := NewDockerfile().Extract(NewFile("app/Dockerfile", []byte(multiStage)))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %+v, want the two real base images only", refs)
	}

	build, runtime := refs[0], refs[1]
	if build.Chart != "Dockerfile/build" || build.Name != "golang" ||
		build.Repository != "index.docker.io/library/golang" || build.CurrentVersion != "1.24" {
		t.Errorf("build stage = %+v", build)
	}
	if build.FinalStage {
		t.Error("build stage flagged final, want the last real base image flagged")
	}
	if runtime.Chart != "Dockerfile/runtime" || runtime.Name != "alpine" || runtime.CurrentVersion != "3.20" {
		t.Errorf("runtime stage (continuation line) = %+v", runtime)
	}
	if !runtime.FinalStage {
		t.Error("runtime stage not flagged final")
	}
	for _, r := range refs {
		if r.Protocol != ProtocolImage {
			t.Errorf("Protocol = %q, want %q", r.Protocol, ProtocolImage)
		}
	}
}

func TestDockerfileExtractRefs(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []Ref // in order; Chart filled in below by path base
	}{
		{
			name:    "unnamed stages are numbered",
			content: "FROM golang:1.24\nFROM alpine:3.20\n",
			want: []Ref{
				{Chart: "Dockerfile/stage1", Name: "golang", Repository: "index.docker.io/library/golang", CurrentVersion: "1.24"},
				{Chart: "Dockerfile/stage2", Name: "alpine", Repository: "index.docker.io/library/alpine", CurrentVersion: "3.20", FinalStage: true},
			},
		},
		{
			name:    "no tag means latest to docker, nothing pinned here",
			content: "FROM nginx\n",
			want: []Ref{
				{Chart: "Dockerfile/stage1", Name: "nginx", Repository: "index.docker.io/library/nginx", FinalStage: true},
			},
		},
		{
			name:    "digest pin",
			content: "FROM alpine@sha256:0be442b25cfb4eccd14ac97a84a20b424e1d020996f9e0c5309056b720c67a02\n",
			want: []Ref{{
				Chart: "Dockerfile/stage1", Name: "alpine", Repository: "index.docker.io/library/alpine",
				Digest: "sha256:0be442b25cfb4eccd14ac97a84a20b424e1d020996f9e0c5309056b720c67a02", FinalStage: true,
			}},
		},
		{
			name:    "private registry with a port keeps its host",
			content: "FROM 10.0.0.4:5000/internal/app:2.1\n",
			want: []Ref{{
				Chart: "Dockerfile/stage1", Name: "10.0.0.4:5000/internal/app",
				Repository: "10.0.0.4:5000/internal/app", CurrentVersion: "2.1", FinalStage: true,
			}},
		},
		{
			name:    "flags, aliases and case-insensitive instructions",
			content: "FROM\t--platform=linux/arm64\t--keep-git-dir=true\tghcr.io/org/tool:v9 AS tool\n",
			want: []Ref{{
				Chart: "Dockerfile/tool", Name: "ghcr.io/org/tool", Repository: "ghcr.io/org/tool",
				CurrentVersion: "v9", FinalStage: true,
			}},
		},
		{
			name:    "a stage may share its base image's name",
			content: "FROM alpine:3.20 AS alpine\nRUN true\n",
			want: []Ref{{
				Chart: "Dockerfile/alpine", Name: "alpine", Repository: "index.docker.io/library/alpine",
				CurrentVersion: "3.20", FinalStage: true,
			}},
		},
		{
			name:    "scratch, stage references and build args are not registry lookups",
			content: "ARG BASE=alpine\nFROM scratch\nFROM ${BASE}\nFROM $BASE:3.20\n",
			want:    nil,
		},
		{
			name:    "a stage built from an earlier one reports nothing, the last real base stays final",
			content: "FROM golang:1.24 AS build\nFROM build AS final\n",
			want: []Ref{{
				Chart: "Dockerfile/build", Name: "golang", Repository: "index.docker.io/library/golang",
				CurrentVersion: "1.24", FinalStage: true,
			}},
		},
		{
			name:    "no FROM at all",
			content: "# nothing here\nFROMAGE=1\nRUN from_this --help\n",
			want:    nil,
		},
		{
			name:    "CRLF line endings",
			content: "FROM golang:1.24 AS build\r\nFROM alpine:3.20\r\n",
			want: []Ref{
				{Chart: "Dockerfile/build", Name: "golang", Repository: "index.docker.io/library/golang", CurrentVersion: "1.24"},
				{Chart: "Dockerfile/stage2", Name: "alpine", Repository: "index.docker.io/library/alpine", CurrentVersion: "3.20", FinalStage: true},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chartName, refs, err := NewDockerfile().Extract(NewFile("build/Dockerfile", []byte(tc.content)))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if chartName != "" {
				t.Errorf("chartName = %q, want empty (each ref carries its own)", chartName)
			}
			if len(refs) != len(tc.want) {
				t.Fatalf("refs = %+v, want %d", refs, len(tc.want))
			}
			for i, want := range tc.want {
				got := refs[i]
				if got.Chart != want.Chart || got.Name != want.Name ||
					got.Repository != want.Repository || got.CurrentVersion != want.CurrentVersion ||
					got.Digest != want.Digest || got.FinalStage != want.FinalStage {
					t.Errorf("ref[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestDockerfileMatchAndType(t *testing.T) {
	ex := NewDockerfile()
	if ex.Type() != "dockerfile" {
		t.Errorf("Type = %q, want dockerfile", ex.Type())
	}
	if !ex.Match(NewFile("Dockerfile.dev", []byte("whatever"))) {
		t.Error("Match(Dockerfile.dev) = false")
	}
	if ex.Match(NewFile("values.yaml", []byte("image: alpine"))) {
		t.Error("Match(values.yaml) = true, want false")
	}
	if err := ex.PrepareFile(NewFile("Dockerfile", []byte("FROM alpine"))); err != nil {
		t.Errorf("PrepareFile: %v", err)
	}
}

func TestIsScannable(t *testing.T) {
	for _, path := range []string{"apps/release.yaml", "Dockerfile", "compose.yml", "x.yml"} {
		if !IsScannable(path) {
			t.Errorf("IsScannable(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"README.md", "Makefile", ".dockerignore", "app.go", "chart/templates/deployment.tpl"} {
		if IsScannable(path) {
			t.Errorf("IsScannable(%q) = true, want false", path)
		}
	}
}

func TestSplitLogicalLines(t *testing.T) {
	got := splitLogicalLines("# comment\n\n  FROM alpine:3.20 \\\n    AS base\nRUN true\n")
	if len(got) != 2 {
		t.Fatalf("lines = %q, want 2 logical lines", got)
	}
	if !strings.Contains(got[0], "AS base") || strings.Contains(got[0], `\`) {
		t.Errorf("continuation not joined: %q", got[0])
	}
	if got[1] != "RUN true" {
		t.Errorf("line 2 = %q", got[1])
	}
}
