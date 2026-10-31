package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

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

// TestLockReleaseWaitsForTheLease: release() returns only once the Lease no longer names us, so the process can exit without stranding the lock on a dead pod.
func TestLockReleaseWaitsForTheLease(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("update", "leases", func(clienttesting.Action) (bool, runtime.Object, error) {
		time.Sleep(100 * time.Millisecond) // giving up the Lease is an API call, so it cannot have landed the instant release() returns
		return false, nil, nil
	})
	_, release, err := Env{Client: client, Namespace: "ns", Identity: "scan-a"}.Lock(context.Background(), "scan")
	if err != nil {
		t.Fatal(err)
	}
	release()
	lease, err := client.CoordinationV1().Leases("ns").Get(context.Background(), "scan", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Lease after release: %v", err)
	}
	holder := ""
	if lease.Spec.HolderIdentity != nil {
		holder = *lease.Spec.HolderIdentity
	}
	if holder != "" {
		t.Errorf("Lease still held by %q after release()", holder)
	}
}

// TestLockTakesOverAnAbandonedLease: the scan after a holder that died without releasing gets the Lease instead of skipping the scan.
func TestLockTakesOverAnAbandonedLease(t *testing.T) {
	dead, recorded := "scan-a", int32(20)
	old := metav1.NewMicroTime(time.Now().Add(-time.Hour))
	client := fake.NewClientset(&coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "scan", Namespace: "ns"},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &dead,
			LeaseDurationSeconds: &recorded, // client-go waits this long from first reading the record, not since its renewTime
			RenewTime:            &old,
			AcquireTime:          &old,
		},
	})
	_, release, err := Env{Client: client, Namespace: "ns", Identity: "scan-b"}.Lock(context.Background(), "scan")
	if err != nil {
		t.Fatalf("Lock after an abandoned Lease = %v, want the Lease", err)
	}
	release()
}
