package metrics

import (
	"os"

	"k8s.io/client-go/rest"
)

// InCluster reports whether the process is running inside a Kubernetes cluster.
// It probes the in-cluster config and the namespace file without creating a
// client — just enough to decide whether to register metrics.
func InCluster() bool {
	_, err := rest.InClusterConfig()
	if err != nil {
		return false
	}
	// Also verify the namespace file exists (covers edge cases where config
	// succeeds but the pod is not fully mounted).
	_, err = os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	return err == nil
}
