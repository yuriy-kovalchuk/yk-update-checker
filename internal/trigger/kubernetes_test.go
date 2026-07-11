package trigger

import (
	"context"
	"errors"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func newTestKubernetes(objs ...runtime.Object) *Kubernetes {
	return &Kubernetes{
		client:      fake.NewClientset(objs...),
		namespace:   "ns",
		cronJobName: "scanner",
		available:   true,
	}
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

	name, err := kt.Trigger(context.Background())
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

	if _, err := kt.Trigger(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestTriggerRefusedWhileManualJobActive(t *testing.T) {
	kt := newTestKubernetes(testCronJob(), manualJob("scanner-manual-1"))

	if _, err := kt.Trigger(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
}

func TestTriggerAllowedAfterManualJobFinished(t *testing.T) {
	done := manualJob("scanner-manual-1", batchv1.JobCondition{
		Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
	})
	kt := newTestKubernetes(testCronJob(), done)

	if _, err := kt.Trigger(context.Background()); err != nil {
		t.Fatalf("Trigger after finished job: %v", err)
	}
}

func TestSecondTriggerRefused(t *testing.T) {
	kt := newTestKubernetes(testCronJob())

	if _, err := kt.Trigger(context.Background()); err != nil {
		t.Fatalf("first Trigger: %v", err)
	}
	if _, err := kt.Trigger(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Trigger err = %v, want ErrAlreadyRunning", err)
	}
}

func TestRunning(t *testing.T) {
	kt := newTestKubernetes(testCronJob())
	if kt.Running(context.Background()) {
		t.Error("Running = true with no jobs")
	}

	if _, err := kt.Trigger(context.Background()); err != nil {
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
