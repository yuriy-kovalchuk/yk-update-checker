package main

import (
	"errors"
	"testing"

	"github.com/yuriy-kovalchuk/yk-update-checker/internal/scan"
)

// TestFailRestRecordsOnlyTheScanError: a repository the scan never reached is recorded with the scan's error alone, not with the write errors of the repositories recorded before it.
func TestFailRestRecordsOnlyTheScanError(t *testing.T) {
	scanErr := errors.New("scan_timeout reached")
	writeErr := errors.New("report write refused")
	recorded := map[string]error{}
	r := &reached{done: map[string]bool{}, write: func(repo string, _ []scan.Result, err error) error {
		recorded[repo] = err
		if repo == "b" {
			return writeErr
		}
		return nil
	}}
	if err := r.onRepo("a", nil, nil); err != nil {
		t.Fatal(err)
	}

	err := r.failRest([]string{"a", "b", "c"}, scanErr)

	if _, ok := recorded["a"]; !ok || recorded["a"] != nil {
		t.Errorf("a was reached; recorded %v, want its own nil error untouched", recorded["a"])
	}
	for _, repo := range []string{"b", "c"} {
		if recorded[repo] != scanErr {
			t.Errorf("%s recorded %q, want exactly the scan error", repo, recorded[repo])
		}
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("failRest returned %v, want the write error reported", err)
	}
}
