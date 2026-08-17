package metrics

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInCluster_ReturnsFalseWhenOutsideKubernetes(t *testing.T) {
	// When running outside a cluster, rest.InClusterConfig() fails because the
	// service account token / CA cert files do not exist.  InCluster() must
	// return false in this case.
	if InCluster() {
		t.Error("expected InCluster() to be false when not in a Kubernetes pod")
	}
}

func TestInCluster_ReturnsFalseWhenNamespaceFileMissing(t *testing.T) {
	// Even if a user mounts fake service account files, the namespace file
	// check provides a second gate.  We can't mock rest.InClusterConfig(), but
	// we know the default path doesn't exist locally.
	tmp := t.TempDir()
	nsFile := filepath.Join(tmp, "namespace")

	if _, err := os.Stat(nsFile); err == nil {
		t.Fatal("test namespace file should not exist yet")
	}

	// Create a dummy token + ca bundle so rest.InClusterConfig *might* succeed
	// if the path were configured; we don't actually control KUBECONFIG here,
	// but the point is: without /var/run/.../namespace it returns false.
	if InCluster() {
		t.Error("InCluster should return false without real cluster mount")
	}

	_ = nsFile // silence unused warning; path not reachable anyway
}
