package kube

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

// TestLockAllowsOneScan: a second holder is refused while the first holds the Lease, and gets it once released.
func TestLockAllowsOneScan(t *testing.T) {
	client := fake.NewClientset()
	first := Env{Client: client, Namespace: "ns", Identity: "scan-a"}
	second := Env{Client: client, Namespace: "ns", Identity: "scan-b"}

	_, release, err := first.Lock(context.Background(), "scan")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.Lock(context.Background(), "scan"); !errors.Is(err, trigger.ErrAlreadyRunning) {
		t.Fatalf("second Lock while held = %v, want trigger.ErrAlreadyRunning", err)
	}
	release()
	_, release2, err := second.Lock(context.Background(), "scan")
	if err != nil {
		t.Fatalf("Lock after release = %v, want the Lease", err)
	}
	release2()
}
