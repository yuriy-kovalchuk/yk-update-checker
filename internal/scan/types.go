package scan

import (
	"time"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

// Result is the outcome of one dependency version check.
type Result struct {
	Source     string `json:"source"`
	Path       string `json:"path,omitempty"` // file the reference is in, relative to the repo root
	Chart      string `json:"chart"`
	Dependency string `json:"dependency"`
	Type       string `json:"type"`
	// Kind is the manifest kind the reference lives in ("Deployment", "HelmRelease", …); empty for a bare Chart.yaml dependency.
	Kind            string `json:"kind,omitempty"`
	Protocol        string `json:"protocol"`
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	Scope           string `json:"scope"`
	UpdateAvailable bool   `json:"update_available"`
	CheckError      string `json:"check_error,omitempty"`
	// CheckErrorKind classifies CheckError (auth, not_found, unreachable, ...; see internal/errkind) so the dashboard can say what to check.
	CheckErrorKind errkind.Kind `json:"check_error_kind,omitempty"`
	CheckedAt      time.Time    `json:"checked_at"`

	// Image-only fields (empty for chart dependencies).
	TagKind string `json:"tag_kind,omitempty"` // semver, calver, date, build, floating, opaque
	// CurrentResolved is the version a floating tag such as :latest actually points at today, read from OCI image metadata.
	CurrentResolved string `json:"current_resolved,omitempty"`
	// FinalStage marks the FROM line whose stage produces the image a build publishes; intermediate stages are false.
	FinalStage bool `json:"final_stage,omitempty"`
	// ImageDigest is what the checked reference points at now; comparing it across scans reveals that a mutable tag moved.
	ImageDigest string `json:"image_digest,omitempty"`
	// DigestChanged is true when the digest differs from the previous scan, or from the digest the manifest reference pins.
	DigestChanged bool `json:"digest_changed,omitempty"`
	// Note explains how a non-version comparison was decided.
	Note string `json:"note,omitempty"`
	// Skipped lists image tags that outrank the current one by version but were rejected as strays rather than releases.
	Skipped []SkippedTag `json:"skipped,omitempty"`
	// CurrentPushedAt and LatestPushedAt are the tags' last push dates, when the registry's vendor API reports them (Docker Hub, Quay, Harbor).
	CurrentPushedAt *time.Time `json:"current_pushed_at,omitempty"`
	LatestPushedAt  *time.Time `json:"latest_pushed_at,omitempty"`
}

// SkippedTag is a rejected candidate image tag and the registry-level reasons for rejecting it.
type SkippedTag = registry.SkippedTag
