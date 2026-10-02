package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

const maxRecentEvents = 2_000

type Match struct {
	Host       string `json:"host"`
	PathPrefix string `json:"path_prefix"`
	Method     string `json:"method"`
}

type Gate struct {
	Enabled       bool   `json:"enabled"`
	TimeoutMS     int64  `json:"timeout_ms"`
	TimeoutAction string `json:"timeout_action"`
}

type Subject struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Display bool   `json:"display"`
	Match   Match  `json:"match"`
	Gate    Gate   `json:"gate"`
}

type Rule struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	PathPrefix string `json:"path_prefix"`
	Method     string `json:"method"`
	ClientIP   string `json:"client_ip"`
	CreatedAt  int64  `json:"created_at"`
}

type GatewayConfig struct {
	Subjects []Subject `json:"subjects"`
	Rules    []Rule    `json:"rules"`
}

type TrafficEvent struct {
	Phase         string `json:"phase"`
	RequestID     string `json:"request_id"`
	SubjectID     string `json:"subject_id"`
	TimestampMS   int64  `json:"timestamp_ms"`
	Method        string `json:"method,omitempty"`
	Path          string `json:"path,omitempty"`
	ClientIP      string `json:"client_ip,omitempty"`
	RequestBytes  int64  `json:"request_bytes,omitempty"`
	ResponseBytes int64  `json:"response_bytes,omitempty"`
	Status        int    `json:"status,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
	Action        string `json:"action,omitempty"`
}

type Store struct {
	mu         sync.RWMutex
	subject    Subject
	rules      map[string]Rule
	events     []TrafficEvent
	eventStart int
}

func newStore() *Store {
	return &Store{
		subject: Subject{
			ID:      "demo-api",
			Name:    "Demo API",
			Enabled: true,
			Display: true,
			Match:   Match{PathPrefix: "/"},
			Gate:    Gate{Enabled: false, TimeoutMS: 30_000, TimeoutAction: "block"},
		},
		rules: map[string]Rule{},
	}
}

func (s *Store) config() GatewayConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rules := make([]Rule, 0, len(s.rules))
	for _, rule := range s.rules {
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].CreatedAt < rules[j].CreatedAt })
	return GatewayConfig{Subjects: []Subject{s.subject}, Rules: rules}
}

func (s *Store) updateSubject(subject Subject) Subject {
	s.mu.Lock()
	defer s.mu.Unlock()
	subject.ID = "demo-api"
	if subject.Name == "" {
		subject.Name = "Demo API"
	}
	if subject.Match.PathPrefix == "" {
		subject.Match.PathPrefix = "/"
	}
	if subject.Gate.TimeoutAction != "allow" && subject.Gate.TimeoutAction != "block" {
		subject.Gate.TimeoutAction = "block"
	}
	s.subject = subject
	return s.subject
}

func (s *Store) addRule(rule Rule) Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule.ID = "rule-" + time.Now().UTC().Format("20060102T150405.000000000")
	rule.Action = "block"
	rule.CreatedAt = time.Now().UnixMilli()
	s.rules[rule.ID] = rule
	return rule
}

func (s *Store) removeRule(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[id]; !ok {
		return false
	}
	delete(s.rules, id)
	return true
}

func (s *Store) addEvent(event TrafficEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) < maxRecentEvents {
		s.events = append(s.events, event)
		return
	}
	s.events[s.eventStart] = event
	s.eventStart = (s.eventStart + 1) % maxRecentEvents
}

func (s *Store) recentEvents(limit int) []TrafficEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.events) {
		limit = len(s.events)
	}
	result := make([]TrafficEvent, limit)
	start := (s.eventStart + len(s.events) - limit) % len(s.events)
	for i := range result {
		result[i] = s.events[(start+i)%len(s.events)]
	}
	return result
}

type client struct {
	conn *websocket.Conn
	send chan []byte
}

type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
}

func newHub() *Hub { return &Hub{clients: map[*client]struct{}{}} }

func (h *Hub) broadcast(event TrafficEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) == 0 {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	for c := range h.clients {
		select {
		case c.send <- payload:
		default:
			// Visualization is deliberately lossy under overload. It must never
			// apply backpressure to the enforcement or telemetry ingestion path.
		}
	}
}

func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, send: make(chan []byte, 128)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	go func() {
		defer conn.Close()
		for message := range c.send {
			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		}
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

type OpenRestyClient struct {
	baseURL string
	client  *http.Client
}

func (c *OpenRestyClient) putConfig(config GatewayConfig) error {
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, c.baseURL+"/internal/config", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &httpError{status: resp.StatusCode}
	}
	return nil
}

func (c *OpenRestyClient) decide(requestID, action string) error {
	body, _ := json.Marshal(map[string]string{"action": action})
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/internal/pending/"+requestID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &httpError{status: resp.StatusCode}
	}
	return nil
}

func (c *OpenRestyClient) decideBatch(requestIDs []string, action string) error {
	body, err := json.Marshal(map[string]any{"request_ids": requestIDs, "action": action})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/internal/pending/decide-batch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &httpError{status: resp.StatusCode}
	}
	return nil
}

func (c *OpenRestyClient) blockAllPending() error {
	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/internal/pending/block-all", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &httpError{status: resp.StatusCode}
	}
	return nil
}

type httpError struct{ status int }

func (e *httpError) Error() string { return http.StatusText(e.status) }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func phaseName(code byte) (string, bool) {
	switch code {
	case 1:
		return "request_started", true
	case 2:
		return "request_pending", true
	case 3:
		return "request_decided", true
	case 4:
		return "request_timeout", true
	case 5:
		return "request_blocked", true
	case 6:
		return "response_started", true
	case 7:
		return "response_finished", true
	default:
		return "", false
	}
}

func takeFrameText(frame []byte, offset *int) (string, bool) {
	if *offset+2 > len(frame) {
		return "", false
	}
	length := int(binary.BigEndian.Uint16(frame[*offset : *offset+2]))
	*offset += 2
	if *offset+length > len(frame) {
		return "", false
	}
	value := string(frame[*offset : *offset+length])
	*offset += length
	return value, true
}

func decodeHTVP(frame []byte) (TrafficEvent, bool) {
	const headerLength = 38
	if len(frame) < headerLength || !bytes.Equal(frame[:4], []byte("HTVP")) || frame[4] != 1 {
		return TrafficEvent{}, false
	}
	phase, ok := phaseName(frame[5])
	if !ok {
		return TrafficEvent{}, false
	}
	offset := headerLength
	requestID, ok := takeFrameText(frame, &offset)
	if !ok || requestID == "" {
		return TrafficEvent{}, false
	}
	subjectID, ok := takeFrameText(frame, &offset)
	if !ok {
		return TrafficEvent{}, false
	}
	method, ok := takeFrameText(frame, &offset)
	if !ok {
		return TrafficEvent{}, false
	}
	path, ok := takeFrameText(frame, &offset)
	if !ok {
		return TrafficEvent{}, false
	}
	clientIP, ok := takeFrameText(frame, &offset)
	if !ok {
		return TrafficEvent{}, false
	}
	action, ok := takeFrameText(frame, &offset)
	if !ok || offset != len(frame) {
		return TrafficEvent{}, false
	}
	return TrafficEvent{
		Phase:         phase,
		RequestID:     requestID,
		SubjectID:     subjectID,
		TimestampMS:   int64(binary.BigEndian.Uint64(frame[8:16])),
		Method:        method,
		Path:          path,
		ClientIP:      clientIP,
		RequestBytes:  int64(binary.BigEndian.Uint64(frame[16:24])),
		ResponseBytes: int64(binary.BigEndian.Uint64(frame[24:32])),
		Status:        int(binary.BigEndian.Uint16(frame[32:34])),
		DurationMS:    int64(binary.BigEndian.Uint32(frame[34:38])),
		Action:        action,
	}, true
}

func decodeEvent(frame []byte) (TrafficEvent, bool) {
	if event, ok := decodeHTVP(frame); ok {
		return event, true
	}
	var event TrafficEvent
	if err := json.Unmarshal(frame, &event); err != nil || event.RequestID == "" {
		return TrafficEvent{}, false
	}
	return event, true
}

func serveUDP(ctx context.Context, store *Store, hub *Hub) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 9100})
	if err != nil {
		log.Fatalf("listen UDP: %v", err)
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	buffer := make([]byte, 64*1024)
	for {
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("read UDP event: %v", err)
			continue
		}
		event, ok := decodeEvent(buffer[:n])
		if !ok {
			continue
		}
		store.addEvent(event)
		hub.broadcast(event)
	}
}

func main() {
	store := newStore()
	hub := newHub()
	adminURL := strings.TrimRight(os.Getenv("HTTPV_OPENRESTY_ADMIN_URL"), "/")
	if adminURL == "" {
		adminURL = "http://127.0.0.1:8091"
	}
	gatewayURL := strings.TrimRight(os.Getenv("HTTPV_GATEWAY_URL"), "/")
	if gatewayURL == "" {
		gatewayURL = "http://127.0.0.1:8088"
	}
	openresty := &OpenRestyClient{baseURL: adminURL, client: &http.Client{Timeout: 2 * time.Second}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveUDP(ctx, store, hub)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if err := openresty.putConfig(store.config()); err != nil {
				log.Printf("OpenResty config sync pending: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"service": "httpv-control",
			"status":  "ok",
			"console": "http://localhost:5173",
			"health":  "/healthz",
		})
	})
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/ws", hub.serveWS)
	r.Get("/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"config": store.config(), "events": store.recentEvents(500)})
	})
	r.Get("/api/events", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, store.recentEvents(500)) })
	r.Post("/api/demo/burst", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Count   int `json:"count"`
			DelayMS int `json:"delay_ms"`
		}
		if err := decodeJSON(r, &payload); err != nil || payload.Count < 1 || payload.Count > 100 || payload.DelayMS < 0 || payload.DelayMS > 20_000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "count must be 1-100 and delay_ms must be 0-20000"})
			return
		}
		for index := 0; index < payload.Count; index++ {
			go func(sequence int) {
				request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, fmt.Sprintf("%s/demo/slow?ms=%d&burst=%d", gatewayURL, payload.DelayMS, sequence), nil)
				if err != nil {
					return
				}
				response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
				if err == nil {
					_ = response.Body.Close()
				}
			}(index)
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"accepted": payload.Count})
	})
	r.Put("/api/subjects/demo-api", func(w http.ResponseWriter, r *http.Request) {
		var subject Subject
		if err := decodeJSON(r, &subject); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subject payload"})
			return
		}
		subject = store.updateSubject(subject)
		if err := openresty.putConfig(store.config()); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the configuration"})
			return
		}
		writeJSON(w, http.StatusOK, subject)
	})
	r.Post("/api/rules", func(w http.ResponseWriter, r *http.Request) {
		var rule Rule
		if err := decodeJSON(r, &rule); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid rule payload"})
			return
		}
		if rule.PathPrefix == "" && rule.ClientIP == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a path prefix or client IP is required"})
			return
		}
		rule = store.addRule(rule)
		if err := openresty.putConfig(store.config()); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the rule"})
			return
		}
		writeJSON(w, http.StatusCreated, rule)
	})
	r.Delete("/api/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !store.removeRule(chi.URLParam(r, "id")) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
			return
		}
		if err := openresty.putConfig(store.config()); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the configuration"})
			return
		}
		writeJSON(w, http.StatusNoContent, nil)
	})
	r.Post("/api/pending/{id}", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Action string `json:"action"`
		}
		if err := decodeJSON(r, &payload); err != nil || (payload.Action != "allow" && payload.Action != "block") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be allow or block"})
			return
		}
		if err := openresty.decide(chi.URLParam(r, "id"), payload.Action); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "pending request is no longer available"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	})
	r.Post("/api/pending/decide-batch", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			RequestIDs []string `json:"request_ids"`
			Action     string   `json:"action"`
		}
		if err := decodeJSON(r, &payload); err != nil || len(payload.RequestIDs) == 0 || len(payload.RequestIDs) > 5_000 || (payload.Action != "allow" && payload.Action != "block") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid batch decision"})
			return
		}
		if err := openresty.decideBatch(payload.RequestIDs, payload.Action); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the batch decision"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"accepted": len(payload.RequestIDs)})
	})
	r.Post("/api/pending/block-all", func(w http.ResponseWriter, r *http.Request) {
		if err := openresty.blockAllPending(); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the emergency action"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	})

	server := &http.Server{Addr: ":8090", Handler: withCORS(r), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("HTTPV control service listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
