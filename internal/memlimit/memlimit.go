// Package memlimit sizes the Go heap limit from the container's memory limit.
package memlimit

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// HeapShare is the part of the container memory limit given to the Go heap.
const HeapShare = 0.7

// cgroupLimitFiles are where cgroup v2 and v1 publish the memory limit.
var cgroupLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

// Apply sets the Go heap limit to HeapShare of the container memory limit and returns the limit it set.
func Apply() (int64, bool) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0, false
	}
	limit, ok := containerLimit(cgroupLimitFiles)
	if !ok {
		return 0, false
	}
	heap := int64(float64(limit) * HeapShare)
	debug.SetMemoryLimit(heap)
	return heap, true
}

// containerLimit reads the first finite memory limit among files.
func containerLimit(files []string) (int64, bool) {
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(string(data))
		if raw == "max" {
			return 0, false // cgroup v2: no limit
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		// cgroup v1 reports "no limit" as a huge page-aligned number.
		if err != nil || n <= 0 || n >= 1<<62 {
			return 0, false
		}
		return n, true
	}
	return 0, false
}
