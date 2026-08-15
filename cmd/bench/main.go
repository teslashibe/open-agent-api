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
	endpoint := flag.String("endpoint", "http://127.0.0.1:8088/v1/chat/completions", "local endpoint")
	model := flag.String("model", "gpt-5.6-sol", "model alias")
	concurrency := flag.Int("concurrency", 1, "concurrent clients")
	duration := flag.Duration("duration", 5*time.Second, "run duration")
	timeout := flag.Duration("timeout", 2*time.Minute, "request timeout")
	flag.Parse()
	if *concurrency < 1 || *duration <= 0 {
		fmt.Fprintln(os.Stderr, "concurrency and duration must be positive")
		os.Exit(2)
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
				begin := time.Now()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, *endpoint, bytes.NewReader(payload))
				if err != nil {
					return
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer local-benchmark")
				resp, err := client.Do(req)
				elapsed := time.Since(begin)
				if ctx.Err() != nil {
					return
				}
				requests.Add(1)
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						successes.Add(1)
					}
				}
				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(started)
	result := calculateReport(int(requests.Load()), int(successes.Load()), elapsed, latencies, *concurrency, *duration)
	_ = json.NewEncoder(os.Stdout).Encode(result)
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
