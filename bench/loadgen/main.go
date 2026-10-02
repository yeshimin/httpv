package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type result struct {
	latency time.Duration
	status  int
	err     error
}

type report struct {
	URL         string         `json:"url"`
	DurationMS  int64          `json:"duration_ms"`
	Requests    uint64         `json:"requests"`
	Errors      uint64         `json:"errors"`
	RPS         float64        `json:"rps"`
	LatencyMS   map[string]any `json:"latency_ms"`
	StatusCodes map[int]uint64 `json:"status_codes"`
}

func percentile(values []time.Duration, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(percentile*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return float64(values[index]) / float64(time.Millisecond)
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8088/demo/slow?ms=0", "target URL")
	duration := flag.Duration("duration", 10*time.Second, "test duration")
	concurrency := flag.Int("concurrency", 64, "number of client workers")
	rate := flag.Int("rate", 0, "target requests/s; 0 sends as fast as workers allow")
	timeout := flag.Duration("timeout", 10*time.Second, "per-request timeout")
	jsonOutput := flag.Bool("json", false, "print JSON report")
	flag.Parse()

	if *duration <= 0 || *concurrency <= 0 || *rate < 0 {
		fmt.Fprintln(os.Stderr, "duration and concurrency must be positive; rate cannot be negative")
		os.Exit(2)
	}

	transport := &http.Transport{
		MaxIdleConns:        *concurrency * 2,
		MaxIdleConnsPerHost: *concurrency * 2,
		MaxConnsPerHost:     *concurrency * 2,
	}
	client := &http.Client{Transport: transport, Timeout: *timeout}
	deadline := time.Now().Add(*duration)
	jobs := make(chan struct{}, *concurrency*4)
	results := make(chan result, *concurrency*4)
	var workers sync.WaitGroup

	for worker := 0; worker < *concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range jobs {
				started := time.Now()
				request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, *url, nil)
				if err != nil {
					results <- result{err: err}
					continue
				}
				response, err := client.Do(request)
				latency := time.Since(started)
				if err != nil {
					results <- result{latency: latency, err: err}
					continue
				}
				_ = response.Body.Close()
				results <- result{latency: latency, status: response.StatusCode}
			}
		}()
	}

	go func() {
		if *rate == 0 {
			for time.Now().Before(deadline) {
				jobs <- struct{}{}
			}
		} else {
			interval := time.Second / time.Duration(*rate)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for time.Now().Before(deadline) {
				<-ticker.C
				if time.Now().Before(deadline) {
					jobs <- struct{}{}
				}
			}
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()

	latencies := make([]time.Duration, 0, *concurrency*100)
	statuses := map[int]uint64{}
	var requests, errors uint64
	for item := range results {
		requests++
		if item.err != nil {
			errors++
			continue
		}
		latencies = append(latencies, item.latency)
		statuses[item.status]++
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	report := report{
		URL:        *url,
		DurationMS: duration.Milliseconds(),
		Requests:   requests,
		Errors:     errors,
		RPS:        float64(requests) / duration.Seconds(),
		LatencyMS: map[string]any{
			"p50": percentile(latencies, 0.50),
			"p95": percentile(latencies, 0.95),
			"p99": percentile(latencies, 0.99),
		},
		StatusCodes: statuses,
	}
	if *jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(report)
		return
	}
	fmt.Printf("requests=%d errors=%d rps=%.2f p50=%.2fms p95=%.2fms p99=%.2fms statuses=%v\n",
		report.Requests, report.Errors, report.RPS,
		report.LatencyMS["p50"], report.LatencyMS["p95"], report.LatencyMS["p99"], report.StatusCodes)
}
