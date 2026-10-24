package report

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/trigger"
)

// Files stores reports as JSON under one directory, for local runs.
type Files struct{ Dir string }

func (f Files) path(kind, name string) string { return filepath.Join(f.Dir, kind, name+".json") }

// ListRepositoryScans implements Backend.
func (f Files) ListRepositoryScans(_ context.Context) ([]RepositoryScan, error) {
	return readAll[RepositoryScan](filepath.Join(f.Dir, "repositoryscans"))
}

// PutRepositoryScan implements Backend.
func (f Files) PutRepositoryScan(_ context.Context, rs *RepositoryScan) error {
	return writeFile(f.path("repositoryscans", rs.Name), rs)
}

// ListDependencies implements Backend.
func (f Files) ListDependencies(_ context.Context, repoID string) ([]Dependency, error) {
	all, err := readAll[Dependency](filepath.Join(f.Dir, "dependencies"))
	out := all[:0]
	for _, d := range all {
		if repoID == "" || d.Labels[LabelRepoID] == repoID {
			out = append(out, d)
		}
	}
	return out, err
}

// PutDependency implements Backend.
func (f Files) PutDependency(_ context.Context, d *Dependency) error {
	return writeFile(f.path("dependencies", d.Name), d)
}

// DeleteRepositoryScan implements Backend.
func (f Files) DeleteRepositoryScan(_ context.Context, name string) error {
	return remove(f.path("repositoryscans", name))
}

// DeleteDependency implements Backend.
func (f Files) DeleteDependency(_ context.Context, name string) error {
	return remove(f.path("dependencies", name))
}

func remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Lock takes an exclusive lock on <Dir>/.scan.lock (released by the OS if the process dies), or returns trigger.ErrAlreadyRunning.
func (f Files) Lock() (func(), error) {
	if err := os.MkdirAll(f.Dir, 0o750); err != nil {
		return nil, err
	}
	fh, err := os.OpenFile(filepath.Join(f.Dir, ".scan.lock"), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = fh.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, trigger.ErrAlreadyRunning
		}
		return nil, err
	}
	return func() { _ = fh.Close() }, nil
}

func readAll[T any](dir string) ([]T, error) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	out := make([]T, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if v, ok := decode[T](data, p); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// writeFile replaces path atomically, so a reader never sees half a file.
func writeFile(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := errors.Join(tmp.Chmod(0o640), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
