package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	manualJobSelector   = "update-checker/trigger=manual"
	manualJobTTLSeconds = int32(3600)     // finished manual Jobs are cleaned up after an hour
	runningTTL          = 5 * time.Second // how stale a "scan Job active" answer may be
)

// Kubernetes starts a scan as a Job created from the scanner CronJob.
type Kubernetes struct {
	client    kubernetes.Interface
	namespace string
	cronJob   string
	available bool

	mu        sync.Mutex
	runningAt time.Time
	running   bool
}

// NewKubernetes checks once that the CronJob is readable; if not, the trigger reports unavailable.
func NewKubernetes(client kubernetes.Interface, namespace, cronJob string) *Kubernetes {
	kt := &Kubernetes{client: client, namespace: namespace, cronJob: cronJob}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.BatchV1().CronJobs(namespace).Get(ctx, cronJob, metav1.GetOptions{}); err != nil {
		slog.Warn("scan trigger unavailable: cannot read the scanner CronJob", "cronjob", cronJob, "error", err)
		return kt
	}
	kt.available = true
	return kt
}

// Available reports whether the trigger can create Jobs.
func (kt *Kubernetes) Available() bool { return kt.available }

// Trigger creates a Job from the CronJob, or returns ErrAlreadyRunning while one is active.
func (kt *Kubernetes) Trigger(ctx context.Context) error {
	_, err := kt.createJob(ctx)
	return err
}

func (kt *Kubernetes) createJob(ctx context.Context) (string, error) {
	if !kt.available {
		return "", fmt.Errorf("kubernetes trigger not available")
	}
	cronJob, active, err := kt.activeJob(ctx)
	if err != nil {
		return "", err
	}
	if active != "" {
		return "", ErrAlreadyRunning
	}
	ttl := manualJobTTLSeconds
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      manualJobName(kt.cronJob, time.Now()),
			Namespace: kt.namespace,
			Labels:    map[string]string{"app.kubernetes.io/component": "scanner", "update-checker/trigger": "manual"},
		},
		Spec: cronJob.Spec.JobTemplate.Spec,
	}
	if job.Spec.TTLSecondsAfterFinished == nil {
		job.Spec.TTLSecondsAfterFinished = &ttl
	}
	created, err := kt.client.BatchV1().Jobs(kt.namespace).Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return "", ErrAlreadyRunning // same-second double click
	}
	if err != nil {
		return "", fmt.Errorf("create job: %w", err)
	}
	kt.mu.Lock()
	kt.running, kt.runningAt = true, time.Now()
	kt.mu.Unlock()
	slog.Info("scanner job created", "job", created.Name)
	return created.Name, nil
}

// manualJobName keeps Job names within the 63-character label limit.
func manualJobName(cronJob string, now time.Time) string {
	const maxPrefix = 63 - len("-manual-") - 10
	if len(cronJob) > maxPrefix {
		cronJob = strings.TrimSuffix(cronJob[:maxPrefix], "-")
	}
	return fmt.Sprintf("%s-manual-%d", cronJob, now.Unix())
}

// Running reports whether a scanner Job is active, cached for runningTTL.
func (kt *Kubernetes) Running(ctx context.Context) bool {
	if !kt.available {
		return false
	}
	kt.mu.Lock()
	defer kt.mu.Unlock()
	if time.Since(kt.runningAt) < runningTTL {
		return kt.running
	}
	_, active, err := kt.activeJob(ctx)
	kt.running, kt.runningAt = err == nil && active != "", time.Now()
	return kt.running
}

// activeJob returns the CronJob and the name of an unfinished scanner Job, if any.
func (kt *Kubernetes) activeJob(ctx context.Context) (*batchv1.CronJob, string, error) {
	cronJob, err := kt.client.BatchV1().CronJobs(kt.namespace).Get(ctx, kt.cronJob, metav1.GetOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("get cronjob %s: %w", kt.cronJob, err)
	}
	if len(cronJob.Status.Active) > 0 {
		return cronJob, cronJob.Status.Active[0].Name, nil
	}
	jobs, err := kt.client.BatchV1().Jobs(kt.namespace).List(ctx, metav1.ListOptions{LabelSelector: manualJobSelector})
	if err != nil {
		return nil, "", fmt.Errorf("list manual jobs: %w", err)
	}
	for i := range jobs.Items {
		if !jobFinished(&jobs.Items[i]) {
			return cronJob, jobs.Items[i].Name, nil
		}
	}
	return cronJob, "", nil
}

func jobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
