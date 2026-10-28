package report

import (
	"testing"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// TestDigestPinsAreSeparateReferences covers the case of two references that differ
// only by the digest they pin: they are two references and must get two reports.
func TestDigestPinsAreSeparateReferences(t *testing.T) {
	following := scan.Result{Source: "homelab", Type: "image", Path: "apps/web/deployment.yaml",
		Chart: "Deployment/web", Dependency: "index.example/app", CurrentVersion: "1.0.0"}
	pinned := following
	pinned.PinnedDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	otherPin := pinned
	otherPin.PinnedDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	if DependencyName(following) == DependencyName(pinned) || DependencyName(following) == DependencyName(otherPin) || DependencyName(pinned) == DependencyName(otherPin) {
		t.Fatalf("references differing only by their digest pin share a report name: %q == %q", DependencyName(pinned), DependencyName(otherPin))
	}

	samePin := pinned
	if DependencyName(pinned) != DependencyName(samePin) {
		t.Errorf("the same reference got two names across scans: %q and %q", DependencyName(pinned), DependencyName(samePin))
	}
}
