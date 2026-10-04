package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var instanceID = func() string {
	if value := os.Getenv("HTTPV_DEMO_ID"); value != "" {
		return value
	}
	return "default"
}()

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
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-HTTPV-Demo", "upstream")
	w.Header().Set("X-HTTPV-Demo-Instance", instanceID)
	if value := r.Header.Get("X-HTTPV-Echo"); value != "" {
		w.Header().Set("X-HTTPV-Echo", value)
	}
	if raw := r.URL.Query().Get("status"); raw != "" {
		if status, err := strconv.Atoi(raw); err == nil && status >= 100 && status <= 599 {
			w.WriteHeader(status)
		}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":            true,
		"method":        r.Method,
		"path":          r.URL.Path,
		"query":         r.URL.RawQuery,
		"delay_ms":      delay,
		"request_bytes": len(body),
		"body_sha256":   digest,
		"received_at":   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func redirect(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Location", "/redirect-target?from=demo")
	w.WriteHeader(http.StatusFound)
}

func status(w http.ResponseWriter, r *http.Request) {
	code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/status/"))
	if err != nil || code < 100 || code > 599 {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	w.Header().Set("X-HTTPV-Demo", "status")
	w.WriteHeader(code)
}

func stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	for index := 0; index < 3; index++ {
		_, _ = fmt.Fprintf(w, "chunk-%d\n", index)
		flusher.Flush()
		time.Sleep(40 * time.Millisecond)
	}
}

func events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for index := 0; index < 3; index++ {
		_, _ = fmt.Fprintf(w, "event: probe\ndata: %d\n\n", index)
		flusher.Flush()
		time.Sleep(40 * time.Millisecond)
	}
}

var websocketUpgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func websocketEcho(w http.ResponseWriter, r *http.Request) {
	connection, err := websocketUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	for {
		messageType, message, err := connection.ReadMessage()
		if err != nil {
			return
		}
		if err := connection.WriteMessage(messageType, message); err != nil {
			return
		}
	}
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", redirect)
	mux.HandleFunc("/status/", status)
	mux.HandleFunc("/stream", stream)
	mux.HandleFunc("/events", events)
	mux.HandleFunc("/ws", websocketEcho)
	mux.HandleFunc("/demo/", demo)
	mux.HandleFunc("/", demo)
	server := &http.Server{Addr: ":9000", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("demo upstream listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
