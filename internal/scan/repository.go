package scan

import (
	"sync"
	"time"
)

// Repository stores the latest scan results in memory.
type Repository interface {
	Save(results []Result, scannedAt time.Time) error
	Load() (results []Result, scannedAt *time.Time, err error)
}

type repository struct {
	mu        sync.RWMutex
	results   []Result
	scannedAt *time.Time
}

// NewRepository creates a new in-memory Repository.
func NewRepository() Repository {
	return &repository{}
}

func (r *repository) Save(results []Result, scannedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = results
	t := scannedAt
	r.scannedAt = &t
	return nil
}

func (r *repository) Load() ([]Result, *time.Time, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Result, len(r.results))
	copy(out, r.results)
	return out, r.scannedAt, nil
}
