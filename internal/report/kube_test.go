package report

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

func TestKubeBackendRoundTrip(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		repoScans: "RepositoryScanList", dependencies: "DependencyList",
	})
	ctx := context.Background()
	k := Kube{Client: client, Namespace: "ns"}
	w := Writer{Backend: k, History: 3}
	if err := w.Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "1.1", ""), res("b", "2.0", "", "")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Repo(ctx, "homelab", []scan.Result{res("a", "1.0", "1.1", "")}, nil); err != nil {
		t.Fatal(err)
	}
	deps, err := k.ListDependencies(ctx, RepoID("homelab"))
	if err != nil || len(deps) != 1 || deps[0].Status.Dependency != "a" || deps[0].Status.OutdatedSince == nil {
		t.Fatalf("dependencies = %+v, %v; want only a, outdated", deps, err)
	}
	repos, err := k.ListRepositoryScans(ctx)
	if err != nil || len(repos) != 1 || len(repos[0].Status.History) != 2 {
		t.Fatalf("repository scans = %+v, %v; want one with 2 runs", repos, err)
	}
}
