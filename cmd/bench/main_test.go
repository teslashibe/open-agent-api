package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
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

func TestPerformRequestIncludesBodyDrainAndClose(t *testing.T) {
	now := time.Unix(0, 0)
	times := []time.Time{now, now.Add(75 * time.Millisecond)}
	index := 0
	closed := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: &trackingBody{
				Reader:  strings.NewReader("complete"),
				onClose: func() { closed = true },
			},
		}, nil
	})}

	elapsed, status, err := performRequest(context.Background(), client, "http://example.test", nil, func() time.Time {
		got := times[index]
		index++
		return got
	})
	if err != nil {
		t.Fatalf("performRequest() error = %v", err)
	}
	if elapsed != 75*time.Millisecond || status != http.StatusOK || !closed {
		t.Fatalf("performRequest() = %v, %d, closed=%t", elapsed, status, closed)
	}
}

func TestRunRejectsMalformedEndpoint(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-endpoint", "://bad"}, &stdout, &stderr); code == 0 {
		t.Fatalf("run() code = 0, report = %q", stdout.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "valid HTTP or HTTPS URL") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-timeout", "0s"}, &stdout, &stderr); code == 0 {
		t.Fatalf("run() code = 0, report = %q", stdout.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "must be positive") {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type trackingBody struct {
	io.Reader
	onClose func()
}

func (b *trackingBody) Close() error {
	b.onClose()
	return nil
}
