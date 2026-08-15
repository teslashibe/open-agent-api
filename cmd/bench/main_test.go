package main

import (
	"testing"
	"time"
)

func TestCalculateReport(t *testing.T) {
	got := calculateReport(4, 3, 2*time.Second, []time.Duration{
		10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond, 40 * time.Millisecond,
	}, 2, time.Second)
	if got.Errors != 1 || got.ThroughputRPS != 2 || got.LatencyMS["p50"] != 30 || got.LatencyMS["p95"] != 40 {
		t.Fatalf("calculateReport() = %#v", got)
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, .95); got != 0 {
		t.Fatalf("percentile(nil) = %v, want 0", got)
	}
}
