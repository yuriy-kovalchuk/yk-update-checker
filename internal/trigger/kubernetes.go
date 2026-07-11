package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// manualJobSelector matches Jobs created by manual triggers.
const manualJobSelector = "update-checker/trigger=manual"

// manualJobTTLSeconds cleans up finished manual Jobs so they don't accumulate
// (the CronJob's history limits only apply to Jobs it owns).
const manualJobTTLSeconds = int32(3600)

// Kubernetes creates a K8s Job from an existing CronJob template.
// Used in Kubernetes deployments so the dashboard can trigger scans on demand.
type Kubernetes struct {
	client      kubernetes.Interface
	namespace   string
	cronJobName string
	available   bool
}

// NewKubernetes creates a Kubernetes trigger that creates Jobs from the named CronJob.
func NewKubernetes(cronJobName string) *Kubernetes {
	kt := &Kubernetes{cronJobName: cronJobName}

	if cronJobName == "" {
		slog.Info("kubernetes trigger disabled (no cronjob name)")
		return kt
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		slog.Info("kubernetes trigger unavailable (not in cluster)")
		return kt
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Warn("kubernetes trigger: failed to create client", "error", err)
		return kt
	}

	nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		slog.Warn("kubernetes trigger: failed to read namespace", "error", err)
		return kt
	}

	kt.client = client
	kt.namespace = strings.TrimSpace(string(nsBytes))
	kt.available = true

	slog.Info("kubernetes trigger ready", "namespace", kt.namespace, "cronjob", cronJobName)
	return kt
}

// Available reports whether the trigger is ready to create Jobs.
func (kt *Kubernetes) Available() bool { return kt.available }

// Trigger creates a one-off Job from the CronJob template and returns the Job name.
// It returns ErrAlreadyRunning when a scanner Job (scheduled or manual) is still active.
func (kt *Kubernetes) Trigger(ctx context.Context) (string, error) {
	if !kt.available {
		return "", fmt.Errorf("kubernetes trigger not available")
	}

	cronJob, err := kt.client.BatchV1().CronJobs(kt.namespace).Get(ctx, kt.cronJobName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get cronjob %s: %w", kt.cronJobName, err)
	}
	if len(cronJob.Status.Active) > 0 {
		slog.Info("scan trigger skipped, scheduled job active", "job", cronJob.Status.Active[0].Name)
		return "", ErrAlreadyRunning
	}
	if active, err := kt.activeManualJob(ctx); err != nil {
		return "", err
	} else if active != "" {
		slog.Info("scan trigger skipped, manual job active", "job", active)
		return "", ErrAlreadyRunning
	}

	jobName := fmt.Sprintf("%s-manual-%d", kt.cronJobName, time.Now().Unix())
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: kt.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "update-checker",
				"app.kubernetes.io/component": "scanner",
				"update-checker/trigger":      "manual",
			},
			Annotations: map[string]string{
				"cronjob.kubernetes.io/instantiate": "manual",
			},
		},
		Spec: cronJob.Spec.JobTemplate.Spec,
	}

	for i := range job.Spec.Template.Spec.Containers {
		job.Spec.Template.Spec.Containers[i].Env = append(
			job.Spec.Template.Spec.Containers[i].Env,
			corev1.EnvVar{Name: "SCAN_TRIGGER", Value: "manual"},
		)
	}

	if job.Spec.TTLSecondsAfterFinished == nil {
		ttl := manualJobTTLSeconds
		job.Spec.TTLSecondsAfterFinished = &ttl
	}

	created, err := kt.client.BatchV1().Jobs(kt.namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		// Job names have second resolution; a concurrent trigger in the same
		// second means a scan is effectively already starting.
		if apierrors.IsAlreadyExists(err) {
			return "", ErrAlreadyRunning
		}
		return "", fmt.Errorf("create job: %w", err)
	}

	slog.Info("scanner job created", "job", created.Name, "cronjob", kt.cronJobName)
	return created.Name, nil
}

// Running reports whether a scanner Job (scheduled or manual) is currently active.
func (kt *Kubernetes) Running(ctx context.Context) bool {
	if !kt.available {
		return false
	}
	cronJob, err := kt.client.BatchV1().CronJobs(kt.namespace).Get(ctx, kt.cronJobName, metav1.GetOptions{})
	if err == nil && len(cronJob.Status.Active) > 0 {
		return true
	}
	active, err := kt.activeManualJob(ctx)
	return err == nil && active != ""
}

// activeManualJob returns the name of an unfinished manually triggered Job, or "".
func (kt *Kubernetes) activeManualJob(ctx context.Context) (string, error) {
	jobs, err := kt.client.BatchV1().Jobs(kt.namespace).List(ctx, metav1.ListOptions{LabelSelector: manualJobSelector})
	if err != nil {
		return "", fmt.Errorf("list manual jobs: %w", err)
	}
	for i := range jobs.Items {
		if !jobFinished(&jobs.Items[i]) {
			return jobs.Items[i].Name, nil
		}
	}
	return "", nil
}

// jobFinished reports whether the Job has reached a terminal condition.
func jobFinished(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// kubernetesTriggerAdapter wraps Kubernetes to satisfy the Trigger interface.
type kubernetesTriggerAdapter struct {
	kt *Kubernetes
}

// NewKubernetesTrigger returns a Trigger-compatible wrapper around Kubernetes.
func NewKubernetesTrigger(cronJobName string) Trigger {
	return &kubernetesTriggerAdapter{kt: NewKubernetes(cronJobName)}
}

func (a *kubernetesTriggerAdapter) Trigger(ctx context.Context) error {
	_, err := a.kt.Trigger(ctx)
	return err
}

func (a *kubernetesTriggerAdapter) Available() bool { return a.kt.Available() }

func (a *kubernetesTriggerAdapter) Running(ctx context.Context) bool { return a.kt.Running(ctx) }
