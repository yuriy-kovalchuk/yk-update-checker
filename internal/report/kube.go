package report

import (
	"context"
	"encoding/json"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

var (
	repoScans    = schema.GroupVersionResource{Group: Group, Version: "v1alpha1", Resource: "repositoryscans"}
	dependencies = schema.GroupVersionResource{Group: Group, Version: "v1alpha1", Resource: "dependencies"}
)

// Kube stores reports as custom resources in one namespace.
type Kube struct {
	Client    dynamic.Interface
	Namespace string
}

// ListRepositoryScans implements Backend.
func (k Kube) ListRepositoryScans(ctx context.Context) ([]RepositoryScan, error) {
	return list[RepositoryScan](ctx, k, repoScans, "")
}

// PutRepositoryScan implements Backend.
func (k Kube) PutRepositoryScan(ctx context.Context, rs *RepositoryScan) error {
	return k.apply(ctx, repoScans, "RepositoryScan", rs.Name, rs)
}

// ListDependencies implements Backend.
func (k Kube) ListDependencies(ctx context.Context, repoID string) ([]Dependency, error) {
	selector := ""
	if repoID != "" {
		selector = LabelRepoID + "=" + repoID
	}
	return list[Dependency](ctx, k, dependencies, selector)
}

// PutDependency implements Backend.
func (k Kube) PutDependency(ctx context.Context, d *Dependency) error {
	return k.apply(ctx, dependencies, "Dependency", d.Name, d)
}

// DeleteRepositoryScan implements Backend.
func (k Kube) DeleteRepositoryScan(ctx context.Context, name string) error {
	return k.delete(ctx, repoScans, name)
}

// DeleteDependency implements Backend.
func (k Kube) DeleteDependency(ctx context.Context, name string) error {
	return k.delete(ctx, dependencies, name)
}

func (k Kube) delete(ctx context.Context, gvr schema.GroupVersionResource, name string) error {
	err := k.Client.Resource(gvr).Namespace(k.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// apply creates the object, or replaces it when it exists.
func (k Kube) apply(ctx context.Context, gvr schema.GroupVersionResource, kind, name string, obj any) error {
	body, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	u := &unstructured.Unstructured{}
	if err := json.Unmarshal(body, &u.Object); err != nil {
		return err
	}
	labels := u.GetLabels()
	u.Object["metadata"] = map[string]any{} // drop what the server set when this object was read
	u.SetName(name)
	u.SetLabels(labels)
	u.SetAPIVersion(APIVersion)
	u.SetKind(kind)
	u.SetNamespace(k.Namespace)
	res := k.Client.Resource(gvr).Namespace(k.Namespace)
	_, err = res.Create(ctx, u, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	cur, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	u.SetResourceVersion(cur.GetResourceVersion())
	_, err = res.Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func list[T any](ctx context.Context, k Kube, gvr schema.GroupVersionResource, selector string) ([]T, error) {
	// ResourceVersion "0" is served from the API server's cache, not a quorum read from etcd.
	l, err := k.Client.Resource(gvr).Namespace(k.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector, ResourceVersion: "0"})
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(l.Items))
	for _, item := range l.Items {
		data, err := json.Marshal(item.Object)
		if err != nil {
			return nil, err
		}
		if v, ok := decode[T](data, item.GetName()); ok {
			out = append(out, v)
		}
	}
	return out, nil
}
