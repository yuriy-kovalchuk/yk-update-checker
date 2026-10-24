package report

import "context"

// Backend stores reports: the Kubernetes API in a cluster, a directory locally.
type Backend interface {
	ListRepositoryScans(ctx context.Context) ([]RepositoryScan, error)
	PutRepositoryScan(ctx context.Context, rs *RepositoryScan) error
	DeleteRepositoryScan(ctx context.Context, name string) error
	// ListDependencies returns every Dependency, or one repository's when repoID is set.
	ListDependencies(ctx context.Context, repoID string) ([]Dependency, error)
	PutDependency(ctx context.Context, d *Dependency) error
	DeleteDependency(ctx context.Context, name string) error
}
