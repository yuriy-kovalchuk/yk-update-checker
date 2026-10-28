package scan

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/errkind"
	"github.com/yuriy-kovalchuk/yk-update-checker/internal/registry"
)

// Result is the outcome of one dependency version check.
type Result struct {
	// +kubebuilder:validation:MaxLength=253
	Source string `json:"source"`
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path,omitempty"` // file the reference is in, relative to the repo root
	// +kubebuilder:validation:MaxLength=4096
	Chart string `json:"chart"`
	// +kubebuilder:validation:MaxLength=4096
	Dependency string `json:"dependency"`
	// +kubebuilder:validation:MaxLength=63
	Type string `json:"type"`
	// Kind is the manifest kind the reference lives in ("Deployment", "HelmRelease", …); empty for a bare Chart.yaml dependency.
	// +kubebuilder:validation:MaxLength=253
	Kind string `json:"kind,omitempty"`
	// +kubebuilder:validation:MaxLength=63
	Protocol string `json:"protocol"`
	// +kubebuilder:validation:MaxLength=1024
	CurrentVersion string `json:"current_version"`
	// +kubebuilder:validation:MaxLength=1024
	LatestVersion string `json:"latest_version"`
	// +kubebuilder:validation:MaxLength=63
	Scope           string `json:"scope"`
	UpdateAvailable bool   `json:"update_available"`
	// +kubebuilder:validation:MaxLength=32768
	CheckError string `json:"check_error,omitempty"`
	// CheckErrorKind classifies CheckError (auth, not_found, unreachable, ...; see internal/errkind) so the dashboard can say what to check.
	CheckErrorKind errkind.Kind `json:"check_error_kind,omitempty"`
	// CheckedAt is when this reference last changed: a rescan that found nothing new leaves it where it was.
	CheckedAt metav1.Time `json:"checked_at"`

	// TagKind is how an image tag is versioned (semver, calver, date, build, floating, opaque); empty for charts.
	// +kubebuilder:validation:MaxLength=63
	TagKind string `json:"tag_kind,omitempty"`
	// CurrentResolved is the version a floating tag such as :latest actually points at today, read from OCI image metadata.
	// +kubebuilder:validation:MaxLength=1024
	CurrentResolved string `json:"current_resolved,omitempty"`
	// FinalStage marks the FROM line whose stage produces the image a build publishes; intermediate stages are false.
	FinalStage bool `json:"final_stage,omitempty"`
	// ImageDigest is what the checked reference points at now; comparing it across scans reveals that a mutable tag moved.
	// +kubebuilder:validation:MaxLength=1024
	ImageDigest string `json:"image_digest,omitempty"`
	// DigestChanged is true when the digest differs from the previous scan, or from the digest the manifest reference pins.
	DigestChanged bool `json:"digest_changed,omitempty"`
	// Note explains how a non-version comparison was decided.
	// +kubebuilder:validation:MaxLength=32768
	Note string `json:"note,omitempty"`
	// Skipped lists image tags that outrank the current one by version but were rejected as strays rather than releases.
	// +kubebuilder:validation:MaxItems=50
	Skipped []SkippedTag `json:"skipped,omitempty"`
	// CurrentPushedAt and LatestPushedAt are the tags' last push dates, when the registry's vendor API reports them (Docker Hub, Quay, Harbor).
	CurrentPushedAt *metav1.Time `json:"current_pushed_at,omitempty"`
	LatestPushedAt  *metav1.Time `json:"latest_pushed_at,omitempty"`
}

// SkippedTag is a rejected candidate image tag and the registry-level reasons for rejecting it.
type SkippedTag = registry.SkippedTag
