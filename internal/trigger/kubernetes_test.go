package trigger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func newTestKubernetes(objs ...runtime.Object) *Kubernetes {
	return NewKubernetes(fake.NewClientset(objs...), "ns", "scanner")
}

func testCronJob() *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "scanner", Namespace: "ns"},
	}
}

func manualJob(name string, conditions ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "ns",
			Labels:    map[string]string{"update-checker/trigger": "manual"},
		},
		Status: batchv1.JobStatus{Conditions: conditions},
	}
}

func TestTriggerCreatesJob(t *testing.T) {
	kt := newTestKubernetes(testCronJob())

	name, err := kt.createJob(context.Background())
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !strings.HasPrefix(name, "scanner-manual-") {
		t.Errorf("job name %q, want prefix scanner-manual-", name)
	}

	job, err := kt.client.BatchV1().Jobs("ns").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created job: %v", err)
	}
	if job.Labels["update-checker/trigger"] != "manual" {
		t.Errorf("missing manual trigger label: %v", job.Labels)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != manualJobTTLSeconds {
		t.Errorf("TTLSecondsAfterFinished = %v, want %d", job.Spec.TTLSecondsAfterFinished, manualJobTTLSeconds)
	}
}

func TestTriggerRefusedWhileScheduledJobActive(t *testing.T) {
	cj := testCronJob()
	cj.Status.Active = []corev1.ObjectReference{{Name: "scanner-29200000"}}
	kt := newTestKubernetes(cj)

	if _, err := kt.createJob(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestTriggerRefusedWhileManualJobActive(t *testing.T) {
	kt := newTestKubernetes(testCronJob(), manualJob("scanner-manual-1"))

	if _, err := kt.createJob(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestTriggerAllowedAfterManualJobFinished(t *testing.T) {
	done := manualJob("scanner-manual-1", batchv1.JobCondition{
		Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
	})
	kt := newTestKubernetes(testCronJob(), done)

	if _, err := kt.createJob(context.Background()); err != nil {
		t.Fatalf("Trigger after finished job: %v", err)
	}
}

func TestSecondTriggerRefused(t *testing.T) {
	kt := newTestKubernetes(testCronJob())

	if _, err := kt.createJob(context.Background()); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	if _, err := kt.createJob(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Trigger err = %v, want ErrAlreadyRunning", err)
	}
}

func TestRunning(t *testing.T) {
	kt := newTestKubernetes(testCronJob())
	if kt.Running(context.Background()) {
		t.Error("Running = true with no jobs")
	}

	if _, err := kt.createJob(context.Background()); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if !kt.Running(context.Background()) {
		t.Error("Running = false while manual job active")
	}

	failed := manualJob("scanner-manual-old", batchv1.JobCondition{
		Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
	})
	kt2 := newTestKubernetes(testCronJob(), failed)
	if kt2.Running(context.Background()) {
		t.Error("Running = true with only finished jobs")
	}
}

func TestManualJobNameFitsTheLabelLimit(t *testing.T) {
	now := time.Unix(1761319999, 0)
	if got := manualJobName("yk-update-checker-scanner", now); got != "yk-update-checker-scanner-manual-1761319999" {
		t.Errorf("short name changed: %q", got)
	}
	long := strings.Repeat("a", 43) + "-b-scanner" // 52, the CronJob maximum
	got := manualJobName(long, now)
	if len(got) > 63 || strings.Contains(got, "--") {
		t.Errorf("manualJobName(%d chars) = %q (%d chars), want <= 63 and no empty segment", len(long), got, len(got))
	}
}

func TestUnavailableWithoutCronJob(t *testing.T) {
	if !newTestKubernetes(testCronJob()).Available() {
		t.Error("unavailable with the CronJob present")
	}
	if newTestKubernetes().Available() {
		t.Error("available with no CronJob to create Jobs from")
	}
}

// TestRunningIsCached (B10): dashboard polls must not each cost a Get and a List against the API server.
func TestRunningIsCached(t *testing.T) {
	kt := newTestKubernetes(testCronJob())
	client := kt.client.(*fake.Clientset)
	client.ClearActions() // ignore the startup check
	for range 5 {
		kt.Running(context.Background())
	}
	if n := len(client.Actions()); n != 2 { // one Get cronjob + one List jobs
		t.Errorf("5 Running calls made %d API calls, want 2 (one uncached answer)", n)
	}
	// A trigger is news: the next poll must say "scanning" without waiting out the cache.
	if _, err := kt.createJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !kt.Running(context.Background()) {
		t.Error("Running = false right after a trigger")
	}
}
