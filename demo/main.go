package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
)

func demo(w http.ResponseWriter, r *http.Request) {
	delay := 100
	if raw := r.URL.Query().Get("ms"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			delay = parsed
		}
	}
	if delay < 0 {
		delay = 0
	}
	if delay > 20_000 {
		delay = 20_000
	}
	time.Sleep(time.Duration(delay) * time.Millisecond)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-HTTPV-Demo", "upstream")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"path":        r.URL.Path,
		"delay_ms":    delay,
		"received_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/demo/", demo)
	mux.HandleFunc("/", demo)
	server := &http.Server{Addr: ":9000", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("demo upstream listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
