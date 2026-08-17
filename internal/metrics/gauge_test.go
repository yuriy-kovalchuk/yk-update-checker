package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestGauge_RecordSuccess(t *testing.T) {
	reg := mustNewRegistry(t)
	g := NewGauge(reg)

	before := time.Now().Unix()
	g.Record("success", 3*time.Second)
	after := time.Now().Unix()

	total := testutil.CollectAndCount(reg, "update_checker_scan_total")
	if total != 1 {
		t.Errorf("expected 1 scan_total, got %d", total)
	}

	lastScanTime := testutil.CollectAndCount(reg, "update_checker_last_scan_time")
	if lastScanTime != 1 {
		t.Errorf("expected 1 last_scan_time metric, got %d", lastScanTime)
	}

	duration := testutil.CollectAndCount(reg, "update_checker_scan_duration_seconds")
	if duration == 0 {
		t.Error("expected scan_duration_seconds metrics, got 0")
	}

	// Verify last_scan_time is in the expected range.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	for _, f := range families {
		if *f.Name == "update_checker_last_scan_time" && len(f.Metric) > 0 {
			ts := int64(f.Metric[0].GetGauge().GetValue())
			if ts < before || ts > after {
				t.Errorf("last_scan_time %d not in range [%d, %d]", ts, before, after)
			}
		}
	}
}

func TestGauge_RecordFailure(t *testing.T) {
	reg := mustNewRegistry(t)
	g := NewGauge(reg)

	g.Record("failure", 500*time.Millisecond)

	total := testutil.CollectAndCount(reg, "update_checker_scan_total")
	if total != 1 {
		t.Errorf("expected 1 scan_total, got %d", total)
	}

	// Verify the counter has status="failure" label.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}
	for _, f := range families {
		if *f.Name == "update_checker_scan_total" && len(f.Metric) > 0 {
			for _, l := range f.Metric[0].Label {
				if l.GetName() == "status" && l.GetValue() != "failure" {
					t.Errorf("expected status=failure, got status=%s", l.GetValue())
				}
			}
		}
	}

	// last_scan_time should NOT be updated on failure.
	lastScanFamilies, _ := reg.Gather()
	for _, f := range lastScanFamilies {
		if *f.Name == "update_checker_last_scan_time" && len(f.Metric) > 0 {
			ts := int64(f.Metric[0].GetGauge().GetValue())
			if ts != 0 {
				t.Errorf("last_scan_time should be 0 after failure, got %d", ts)
			}
		}
	}
}

func TestGauge_MultipleScans(t *testing.T) {
	reg := mustNewRegistry(t)
	g := NewGauge(reg)

	g.Record("success", 2*time.Second)
	g.Record("success", 3*time.Second)
	g.Record("failure", 100*time.Millisecond)
	g.Record("success", 4*time.Second)

	// Counter sums should reflect all scans.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	successCount := 0.0
	failureCount := 0.0
	for _, f := range families {
		if *f.Name == "update_checker_scan_total" {
			for _, m := range f.Metric {
				status := ""
				for _, l := range m.Label {
					if l.GetName() == "status" {
						status = l.GetValue()
					}
				}
				switch status {
				case "success":
					successCount += m.Counter.GetValue()
				case "failure":
					failureCount += m.Counter.GetValue()
				}
			}
		}
	}

	if successCount != 3 {
		t.Errorf("expected 3 successful scans, got %f", successCount)
	}
	if failureCount != 1 {
		t.Errorf("expected 1 failed scan, got %f", failureCount)
	}
}

func TestGauge_DurationObserved(t *testing.T) {
	reg := mustNewRegistry(t)
	g := NewGauge(reg)

	g.Record("success", 2500*time.Millisecond)
	g.Record("success", 5000*time.Millisecond)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather failed: %v", err)
	}

	for _, f := range families {
		if *f.Name == "update_checker_scan_duration_seconds" {
			// Summary should have count=2 and sample_sum ≈ 7.5s.
			if len(f.Metric) == 0 {
				t.Fatal("no scan_duration_seconds metrics")
			}
			s := f.Metric[0].Summary
			if s.GetSampleCount() != 2 {
				t.Errorf("expected sample count 2, got %d", s.GetSampleCount())
			}
			if s.GetSampleSum() < 7.4 || s.GetSampleSum() > 7.6 {
				t.Errorf("expected sample sum ≈ 7.5, got %f", s.GetSampleSum())
			}
		}
	}
}
