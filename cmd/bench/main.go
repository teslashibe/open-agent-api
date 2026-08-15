// Command bench runs a bounded local OpenAI-compatible workload and emits JSON.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type report struct {
	Requests       int                `json:"requests"`
	Successes      int                `json:"successes"`
	Errors         int                `json:"errors"`
	DurationMS     float64            `json:"duration_ms"`
	ThroughputRPS  float64            `json:"throughput_rps"`
	LatencyMS      map[string]float64 `json:"latency_ms"`
	Concurrency    int                `json:"concurrency"`
	ConfiguredTime string             `json:"configured_duration"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bench", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "http://127.0.0.1:8088/v1/chat/completions", "local endpoint")
	model := flags.String("model", "gpt-5.6-sol", "model alias")
	concurrency := flags.Int("concurrency", 1, "concurrent clients")
	duration := flags.Duration("duration", 5*time.Second, "run duration")
	timeout := flags.Duration("timeout", 2*time.Minute, "request timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	parsedEndpoint, err := url.ParseRequestURI(*endpoint)
	if err != nil || (parsedEndpoint.Scheme != "http" && parsedEndpoint.Scheme != "https") || parsedEndpoint.Host == "" {
		fmt.Fprintln(stderr, "endpoint must be a valid HTTP or HTTPS URL")
		return 2
	}
	if *concurrency < 1 || *duration <= 0 || *timeout <= 0 {
		fmt.Fprintln(stderr, "concurrency, duration, and timeout must be positive")
		return 2
	}

	payload, _ := json.Marshal(map[string]any{
		"model": *model, "stream": false,
		"messages": []map[string]string{{"role": "user", "content": "Reply with exactly: benchmark-ok"}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	client := &http.Client{Timeout: *timeout}
	started := time.Now()
	var requests, successes atomic.Int64
	var mu sync.Mutex
	var latencies []time.Duration
	var wg sync.WaitGroup
	for range *concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				elapsed, statusCode, err := performRequest(ctx, client, *endpoint, payload, time.Now)
				if err != nil {
					requests.Add(1)
					mu.Lock()
					latencies = append(latencies, elapsed)
					mu.Unlock()
					if ctx.Err() != nil {
						return
					}
					continue
				}
				requests.Add(1)
				if statusCode >= 200 && statusCode < 300 {
					successes.Add(1)
				}
				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()
				if ctx.Err() != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(started)
	result := calculateReport(int(requests.Load()), int(successes.Load()), elapsed, latencies, *concurrency, *duration)
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintf(stderr, "encode report: %v\n", err)
		return 1
	}
	return 0
}

func performRequest(ctx context.Context, client *http.Client, endpoint string, payload []byte, now func() time.Time) (time.Duration, int, error) {
	begin := now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return now().Sub(begin), 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer local-benchmark")
	resp, err := client.Do(req)
	if err != nil {
		return now().Sub(begin), 0, err
	}
	_, copyErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	elapsed := now().Sub(begin)
	if copyErr != nil {
		return elapsed, resp.StatusCode, copyErr
	}
	if closeErr != nil {
		return elapsed, resp.StatusCode, closeErr
	}
	return elapsed, resp.StatusCode, nil
}

func calculateReport(requests, successes int, elapsed time.Duration, latencies []time.Duration, concurrency int, configured time.Duration) report {
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return report{
		Requests: requests, Successes: successes, Errors: requests - successes,
		DurationMS:    float64(elapsed) / float64(time.Millisecond),
		ThroughputRPS: float64(requests) / elapsed.Seconds(),
		LatencyMS: map[string]float64{
			"p50": percentile(latencies, 0.50), "p95": percentile(latencies, 0.95), "p99": percentile(latencies, 0.99),
		},
		Concurrency: concurrency, ConfiguredTime: configured.String(),
	}
}

func percentile(values []time.Duration, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1)*quantile + 0.5)
	return float64(values[index]) / float64(time.Millisecond)
}
