// Package trigger provides manual scan triggering via K8s CronJob.
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// KubernetesTrigger creates Jobs from an existing CronJob.
type KubernetesTrigger struct {
	client      kubernetes.Interface
	namespace   string
	cronJobName string
	available   bool
}

// NewKubernetesTrigger creates a trigger that creates Jobs from an existing CronJob.
// cronJobName is the name of the CronJob to create Jobs from.
func NewKubernetesTrigger(cronJobName string) *KubernetesTrigger {
	kt := &KubernetesTrigger{
		cronJobName: cronJobName,
		available:   false,
	}

	if cronJobName == "" {
		slog.Info("kubernetes trigger not configured (no cronjob name)")
		return kt
	}

	// Try to get in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		slog.Info("kubernetes trigger not available (not in cluster)")
		return kt
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		slog.Warn("failed to create kubernetes client", "error", err)
		return kt
	}

	// Read namespace from mounted service account
	nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		slog.Warn("failed to read namespace", "error", err)
		return kt
	}

	kt.client = client
	kt.namespace = strings.TrimSpace(string(nsBytes))
	kt.available = true

	slog.Info("kubernetes trigger initialized",
		"namespace", kt.namespace,
		"cronjob", cronJobName,
	)
	return kt
}

// Available returns true if the trigger can create Jobs.
func (kt *KubernetesTrigger) Available() bool {
	return kt.available
}

// Trigger creates a new Job from the CronJob template.
func (kt *KubernetesTrigger) Trigger(ctx context.Context) (string, error) {
	if !kt.available {
		return "", fmt.Errorf("kubernetes trigger not available")
	}

	// Get the CronJob to extract its job template
	cronJob, err := kt.client.BatchV1().CronJobs(kt.namespace).Get(ctx, kt.cronJobName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get cronjob %s: %w", kt.cronJobName, err)
	}

	// Create Job from CronJob template
	jobName := fmt.Sprintf("%s-manual-%d", kt.cronJobName, time.Now().Unix())

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: kt.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "update-checker-scanner",
				"app.kubernetes.io/component": "scanner",
				"update-checker-scanner/trigger":          "manual",
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

	created, err := kt.client.BatchV1().Jobs(kt.namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("create job: %w", err)
	}

	slog.Info("scanner job created from cronjob",
		"job", created.Name,
		"cronjob", kt.cronJobName,
		"namespace", kt.namespace,
	)
	return created.Name, nil
}
