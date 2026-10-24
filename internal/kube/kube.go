// Package kube connects to the cluster the process runs in.
package kube

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

// Env is the pod's ServiceAccount connection, namespace and identity.
type Env struct {
	Config    *rest.Config
	Client    kubernetes.Interface
	Namespace string
	Identity  string // the pod name: tells Lease holders apart
}

// Connect uses the ServiceAccount mounted into the pod.
func Connect() (Env, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return Env{}, err
	}
	ns, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return Env{}, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Env{}, err
	}
	id, err := os.Hostname()
	return Env{Config: cfg, Client: client, Namespace: strings.TrimSpace(string(ns)), Identity: id}, err
}

// Lease timings: a holder that stops renewing is replaced after leaseDuration.
const (
	leaseDuration = 15 * time.Second
	retryPeriod   = 2 * time.Second
)

// Leader campaigns for the Lease named lease until ctx ends; the result reports whether this pod leads.
func (e Env) Leader(ctx context.Context, lease string) (func() bool, error) {
	var leading atomic.Bool
	elector, err := e.elector(lease, func(context.Context) { leading.Store(true) }, func() { leading.Store(false) })
	if err != nil {
		return nil, err
	}
	go func() {
		for ctx.Err() == nil {
			elector.Run(ctx) // returns when leadership is lost; campaign again
		}
	}()
	return leading.Load, nil
}

// Lock holds the Lease named lease, or returns trigger.ErrAlreadyRunning; the returned context ends if the Lease is lost.
func (e Env) Lock(ctx context.Context, lease string) (context.Context, context.CancelFunc, error) {
	ctx, release := context.WithCancel(ctx)
	held := make(chan context.Context, 1)
	elector, err := e.elector(lease, func(c context.Context) { held <- c; <-c.Done() }, release)
	if err != nil {
		release()
		return nil, nil, err
	}
	go elector.Run(ctx)
	select {
	case c := <-held:
		return c, release, nil
	case <-time.After(leaseDuration + retryPeriod): // long enough to take over a crashed holder's Lease
		release()
		return nil, nil, trigger.ErrAlreadyRunning
	}
}

func (e Env) elector(lease string, started func(context.Context), stopped func()) (*leaderelection.LeaderElector, error) {
	return leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: lease, Namespace: e.Namespace},
			Client:     e.Client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: e.Identity},
		},
		LeaseDuration:   leaseDuration,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     retryPeriod,
		ReleaseOnCancel: true,
		Callbacks:       leaderelection.LeaderCallbacks{OnStartedLeading: started, OnStoppedLeading: stopped},
	})
}
