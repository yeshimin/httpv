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
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

const (
	maxRecentEvents         = 2_000
	defaultVisualDetailRate = 360
	visualSummaryInterval   = 500 * time.Millisecond
	visualRecordTTL         = time.Minute
	hubPriorityQueueSize    = 2_048
	hubDetailQueueSize      = 16_384
	udpReadBufferBytes      = 16 << 20
	demoMaxConcurrency      = 128
	demoQueueSize           = 4_096
)

type Match struct {
	Host       string `json:"host"`
	PathPrefix string `json:"path_prefix"`
	Method     string `json:"method"`
}

type Gate struct {
	Enabled        bool   `json:"enabled"`
	TimeoutMS      int64  `json:"timeout_ms"`
	TimeoutAction  string `json:"timeout_action"`
	MaxPending     int64  `json:"max_pending"`
	OverflowAction string `json:"overflow_action"`
	FailureAction  string `json:"failure_action"`
}

type Subject struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Enabled bool          `json:"enabled"`
	Display bool          `json:"display"`
	Match   Match         `json:"match"`
	Gate    Gate          `json:"gate"`
	Capture CapturePolicy `json:"capture"`
}

type CapturePolicy struct {
	Enabled         bool     `json:"enabled"`
	RequestHeaders  bool     `json:"request_headers"`
	ResponseHeaders bool     `json:"response_headers"`
	RequestBody     bool     `json:"request_body"`
	ResponseBody    bool     `json:"response_body"`
	MaxBodyBytes    int64    `json:"max_body_bytes"`
	TTLSeconds      int64    `json:"ttl_seconds"`
	SampleRate      float64  `json:"sample_rate"`
	ContentTypes    []string `json:"content_types"`
	RedactHeaders   []string `json:"redact_headers"`
}

type Upstream struct {
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	URL              string           `json:"url,omitempty"`
	HostHeader       string           `json:"host_header,omitempty"`
	Targets          []UpstreamTarget `json:"targets"`
	MaxFailures      int64            `json:"max_failures"`
	FailureTimeoutMS int64            `json:"failure_timeout_ms"`
}

type UpstreamTarget struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	HostHeader string `json:"host_header"`
	Weight     int64  `json:"weight"`
}

type Route struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Match      Match  `json:"match"`
	UpstreamID string `json:"upstream_id"`
}

type Rule struct {
	ID         string `json:"id"`
	SubjectID  string `json:"subject_id,omitempty"`
	Action     string `json:"action"`
	PathPrefix string `json:"path_prefix"`
	Method     string `json:"method"`
	ClientIP   string `json:"client_ip"`
	CreatedAt  int64  `json:"created_at"`
}

type GatewayConfig struct {
	SchemaVersion int        `json:"schema_version"`
	Upstreams     []Upstream `json:"upstreams"`
	Routes        []Route    `json:"routes"`
	Subjects      []Subject  `json:"subjects"`
	Rules         []Rule     `json:"rules"`
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

// TrafficSummary is a visualization-only overload signal. It never replaces
// OpenResty's per-request policy evaluation or the control plane's counters.
type TrafficSummary struct {
	Type          string               `json:"type"`
	WindowStartMS int64                `json:"window_start_ms"`
	WindowEndMS   int64                `json:"window_end_ms"`
	Total         uint64               `json:"total"`
	Started       uint64               `json:"started"`
	Responded     uint64               `json:"responded"`
	Finished      uint64               `json:"finished"`
	Pending       uint64               `json:"pending"`
	Blocked       uint64               `json:"blocked"`
	Paths         []TrafficPathSummary `json:"paths"`
}

type TrafficPathSummary struct {
	SubjectID string `json:"subject_id"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Count     uint64 `json:"count"`
	Blocked   uint64 `json:"blocked"`
	Pending   uint64 `json:"pending"`
}

type trafficSummaryAccumulator struct {
	windowStartMS int64
	total         uint64
	started       uint64
	responded     uint64
	finished      uint64
	pending       uint64
	blocked       uint64
	paths         map[string]*TrafficPathSummary
}

func (a *trafficSummaryAccumulator) add(event TrafficEvent) {
	if a.windowStartMS == 0 {
		a.windowStartMS = event.TimestampMS
	}
	a.total++
	switch event.Phase {
	case "request_started":
		a.started++
	case "response_started":
		a.responded++
	case "response_finished":
		a.finished++
	case "request_pending":
		a.pending++
	case "request_blocked":
		a.blocked++
	case "request_overflow":
		if event.Action != "allow" {
			a.blocked++
		}
	}
	key := event.SubjectID + "\x00" + event.Method + "\x00" + event.Path
	path := a.paths[key]
	if path == nil {
		if len(a.paths) >= 32 {
			key = "\x00\x00other"
			path = a.paths[key]
		}
		if path == nil {
			path = &TrafficPathSummary{SubjectID: event.SubjectID, Method: event.Method, Path: event.Path}
			if key == "\x00\x00other" {
				path = &TrafficPathSummary{Path: "other"}
			}
			a.paths[key] = path
		}
	}
	path.Count++
	if event.Phase == "request_blocked" || (event.Phase == "request_overflow" && event.Action != "allow") {
		path.Blocked++
	}
	if event.Phase == "request_pending" {
		path.Pending++
	}
}

func (a *trafficSummaryAccumulator) take(windowEndMS int64) *TrafficSummary {
	if a == nil || a.total == 0 {
		return nil
	}
	paths := make([]TrafficPathSummary, 0, len(a.paths))
	for _, path := range a.paths {
		paths = append(paths, *path)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Count > paths[j].Count })
	if len(paths) > 6 {
		paths = paths[:6]
	}
	return &TrafficSummary{
		Type:          "traffic_summary",
		WindowStartMS: a.windowStartMS,
		WindowEndMS:   windowEndMS,
		Total:         a.total,
		Started:       a.started,
		Responded:     a.responded,
		Finished:      a.finished,
		Pending:       a.pending,
		Blocked:       a.blocked,
		Paths:         paths,
	}
}

type Store struct {
	mu         sync.RWMutex
	gateway    GatewayConfig
	revision   uint64
	revisions  []ConfigRevision
	events     []TrafficEvent
	eventStart int
}

func newStore() *Store {
	config := defaultGatewayConfig()
	return &Store{
		gateway:   config,
		revision:  1,
		revisions: []ConfigRevision{{Revision: 1, CreatedAt: time.Now().UnixMilli(), Source: "defaults", Config: cloneGatewayConfig(config)}},
	}
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
	if len(s.events) == 0 {
		return []TrafficEvent{}
	}
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
	conn         *websocket.Conn
	priority     chan []byte
	detail       chan []byte
	done         chan struct{}
	detailWindow time.Time
	detailStarts int
	visible      map[string]time.Time
	summary      *trafficSummaryAccumulator
}

type Hub struct {
	mu             sync.Mutex
	clients        map[*client]struct{}
	priorityEvents chan TrafficEvent
	detailEvents   chan TrafficEvent
	detailRate     int
}

func newHub() *Hub {
	detailRate := defaultVisualDetailRate
	if configured, err := strconv.Atoi(os.Getenv("HTTPV_VISUAL_DETAIL_STARTS_PER_SEC")); err == nil && configured > 0 {
		detailRate = configured
	}
	return &Hub{
		clients:        map[*client]struct{}{},
		priorityEvents: make(chan TrafficEvent, hubPriorityQueueSize),
		detailEvents:   make(chan TrafficEvent, hubDetailQueueSize),
		detailRate:     detailRate,
	}
}

func terminalEvent(event TrafficEvent) bool {
	return event.Phase == "response_finished" || event.Phase == "request_aborted"
}

func priorityEvent(event TrafficEvent) bool {
	return event.Phase == "request_pending" || event.Phase == "request_decided" || event.Phase == "request_timeout" || event.Phase == "request_blocked" || event.Phase == "request_aborted" || event.Phase == "request_overflow"
}

func (c *client) aggregate(event TrafficEvent) {
	if c.summary == nil {
		c.summary = &trafficSummaryAccumulator{paths: map[string]*TrafficPathSummary{}}
	}
	c.summary.add(event)
}

func (c *client) shouldShowDetail(event TrafficEvent, now time.Time, detailRate int) bool {
	if _, ok := c.visible[event.RequestID]; ok {
		if terminalEvent(event) {
			delete(c.visible, event.RequestID)
		}
		return true
	}
	if priorityEvent(event) {
		c.visible[event.RequestID] = now
		return true
	}
	if event.Phase != "request_started" {
		return false
	}
	if c.detailWindow.IsZero() || now.Sub(c.detailWindow) >= time.Second {
		c.detailWindow = now
		c.detailStarts = 0
	}
	if c.detailStarts >= detailRate {
		return false
	}
	c.detailStarts++
	c.visible[event.RequestID] = now
	return true
}

func (h *Hub) broadcast(event TrafficEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) == 0 {
		return
	}
	now := time.Now()
	var payload []byte
	for c := range h.clients {
		if !c.shouldShowDetail(event, now, h.detailRate) {
			c.aggregate(event)
			continue
		}
		if payload == nil {
			var err error
			payload, err = json.Marshal(event)
			if err != nil {
				return
			}
		}
		queue := c.detail
		if priorityEvent(event) {
			queue = c.priority
		}
		select {
		case queue <- payload:
		default:
			c.aggregate(event)
		}
	}
}

// enqueue keeps UDP ingestion independent from browser rendering. Under an
// overload, lifecycle decisions retain their own queue while ordinary visual
// detail may be sampled by the already bounded WebSocket path.
func (h *Hub) enqueue(event TrafficEvent) {
	queue := h.detailEvents
	if priorityEvent(event) {
		queue = h.priorityEvents
	}
	select {
	case queue <- event:
	default:
		// Do not stall the telemetry receiver. The Store has already retained
		// the event and the data plane is never coupled to console delivery.
	}
}

func (h *Hub) flushSummaries(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		for id, started := range c.visible {
			if now.Sub(started) > visualRecordTTL {
				delete(c.visible, id)
			}
		}
		summary := c.summary.take(now.UnixMilli())
		if summary == nil {
			continue
		}
		payload, err := json.Marshal(summary)
		if err == nil {
			select {
			case c.priority <- payload:
			default:
			}
		}
		c.summary = nil
	}
}

func (h *Hub) run(ctx context.Context) {
	ticker := time.NewTicker(visualSummaryInterval)
	defer ticker.Stop()
	for {
		// Drain decisions before ordinary detail so operator actions remain
		// visible even while a large traffic burst is being summarized.
		select {
		case event := <-h.priorityEvents:
			h.broadcast(event)
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case event := <-h.priorityEvents:
			h.broadcast(event)
		case event := <-h.detailEvents:
			h.broadcast(event)
		case now := <-ticker.C:
			h.flushSummaries(now)
		}
	}
}

func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: sameOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{
		conn:     conn,
		priority: make(chan []byte, 256),
		detail:   make(chan []byte, 512),
		done:     make(chan struct{}),
		visible:  map[string]time.Time{},
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	go func() {
		defer conn.Close()
		for {
			var message []byte
			select {
			case <-c.done:
				return
			case message = <-c.priority:
			default:
				select {
				case <-c.done:
					return
				case message = <-c.priority:
				case message = <-c.detail:
				}
			}
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
		close(c.done)
	}
	h.mu.Unlock()
}

type OpenRestyClient struct {
	baseURL string
	client  *http.Client
}

type DemoLoadGenerator struct {
	gatewayURL string
	client     *http.Client
	jobs       chan string
	queued     atomic.Int64
	active     atomic.Int64
}

func newDemoLoadGenerator(ctx context.Context, gatewayURL string) *DemoLoadGenerator {
	generator := &DemoLoadGenerator{
		gatewayURL: gatewayURL,
		client: &http.Client{Transport: &http.Transport{
			MaxIdleConns:        demoMaxConcurrency,
			MaxIdleConnsPerHost: demoMaxConcurrency,
			MaxConnsPerHost:     demoMaxConcurrency,
		}},
		jobs: make(chan string, demoQueueSize),
	}
	for index := 0; index < demoMaxConcurrency; index++ {
		go generator.runWorker(ctx)
	}
	return generator
}

func (g *DemoLoadGenerator) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case path := <-g.jobs:
			g.active.Add(1)
			g.queued.Add(-1)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.gatewayURL+path, nil)
			if err == nil {
				response, requestErr := g.client.Do(request)
				if requestErr == nil {
					_ = response.Body.Close()
				}
			}
			g.active.Add(-1)
		}
	}
}

func (g *DemoLoadGenerator) submit(count int, path string) int {
	accepted := 0
	for index := 0; index < count; index++ {
		g.queued.Add(1)
		select {
		case g.jobs <- path:
			accepted++
		default:
			g.queued.Add(-1)
			return accepted
		}
	}
	return accepted
}

func (g *DemoLoadGenerator) status() map[string]int64 {
	return map[string]int64{
		"active": g.active.Load(),
		"queued": g.queued.Load(),
	}
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

func (c *OpenRestyClient) requestDetails(requestID string) (any, error) {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"/internal/details/"+url.PathEscape(requestID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, &httpError{status: resp.StatusCode}
	}
	var details any
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 3<<20))
	if err := decoder.Decode(&details); err != nil {
		return nil, err
	}
	return details, nil
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
		allowedOrigin := strings.TrimSpace(os.Getenv("HTTPV_CORS_ORIGIN"))
		if allowedOrigin != "" && r.Header.Get("Origin") == allowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-HTTPV-CSRF, X-HTTPV-Confirm")
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
	case 8:
		return "request_aborted", true
	case 9:
		return "request_overflow", true
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
	if err := conn.SetReadBuffer(udpReadBufferBytes); err != nil {
		log.Printf("set UDP telemetry receive buffer: %v", err)
	}
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
		hub.enqueue(event)
	}
}

func main() {
	statePath := os.Getenv("HTTPV_CONTROL_STATE_PATH")
	configPath := os.Getenv("HTTPV_CONFIG_PATH")
	store, err := loadStore(statePath, configPath)
	if err != nil {
		log.Printf("control state unavailable; using defaults: %v", err)
		store = newStore()
	}
	hub := newHub()
	adminURL := strings.TrimRight(os.Getenv("HTTPV_OPENRESTY_ADMIN_URL"), "/")
	if adminURL == "" {
		adminURL = "http://127.0.0.1:8091"
	}
	gatewayURL := strings.TrimRight(os.Getenv("HTTPV_GATEWAY_URL"), "/")
	if gatewayURL == "" {
		gatewayURL = "http://127.0.0.1:8088"
	}
	openresty := &OpenRestyClient{baseURL: adminURL, client: &http.Client{Timeout: 10 * time.Second}}
	nativePolicy := newNativePolicyPublisher(os.Getenv("HTTPV_NATIVE_POLICY_PATH"))
	auth := newAuthManagerFromEnv()
	audit := newAuditLogger(os.Getenv("HTTPV_AUDIT_LOG_PATH"))
	persistStore := func() error { return store.persist(statePath) }
	syncGatewayConfig := func() error {
		config := store.config()
		if _, err := nativePolicy.publish(config); err != nil {
			return fmt.Errorf("publish native policy: %w", err)
		}
		return openresty.putConfig(config)
	}
	applyGatewayConfig := func(config GatewayConfig, source string) (uint64, error) {
		revision, err := store.applyConfig(config, source)
		if err != nil {
			return 0, err
		}
		if err := persistStore(); err != nil {
			return 0, fmt.Errorf("persist configuration: %w", err)
		}
		if err := syncGatewayConfig(); err != nil {
			return 0, fmt.Errorf("sync OpenResty: %w", err)
		}
		return revision, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveUDP(ctx, store, hub)
	go hub.run(ctx)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := syncGatewayConfig(); err != nil {
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
	r.Use(auth.middleware)
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
	r.Get("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		_, session, authenticated := auth.session(r)
		response := map[string]any{"enabled": auth.enabled(), "authenticated": authenticated}
		if authenticated {
			response["role"] = session.Role.String()
			response["csrf_token"] = session.CSRFToken
		}
		writeJSON(w, http.StatusOK, response)
	})
	r.Post("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Token string `json:"token"`
		}
		if err := decodeJSON(r, &payload); err != nil {
			audit.record(r, "auth.login", "", "failure")
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid login payload"})
			return
		}
		sessionID, session, ok := auth.login(payload.Token)
		if !ok {
			audit.record(r, "auth.login", "", "failure")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		if auth.enabled() {
			writeSessionCookie(w, r, sessionID, auth.ttl)
		}
		auditRequest := r.WithContext(context.WithValue(r.Context(), authContextKey{}, session.Role))
		audit.record(auditRequest, "auth.login", "", "success")
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "role": session.Role.String(), "csrf_token": session.CSRFToken})
	})
	r.Post("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		sessionID, _ := r.Context().Value(sessionCookieName).(string)
		auth.logout(sessionID)
		clearSessionCookie(w, r)
		audit.record(r, "auth.logout", "", "success")
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
	})
	r.Get("/api/audit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"entries": audit.recent(auditLimit(r))})
	})
	r.Get("/api/requests/{id}/details", func(w http.ResponseWriter, r *http.Request) {
		requestID := chi.URLParam(r, "id")
		details, err := openresty.requestDetails(requestID)
		if err != nil {
			status := http.StatusBadGateway
			if failure, ok := err.(*httpError); ok && failure.status == http.StatusNotFound {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]string{"error": "request details are unavailable"})
			return
		}
		audit.record(r, "details.read", requestID, "success")
		writeJSON(w, http.StatusOK, details)
	})
	r.Get("/api/ws", hub.serveWS)
	r.Get("/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"config": store.config(), "config_revision": store.configRevision(), "events": store.recentEvents(500)})
	})
	r.Get("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-HTTPV-Config-Revision", strconv.FormatUint(store.configRevision(), 10))
		writeJSON(w, http.StatusOK, store.config())
	})
	r.Get("/api/config/revisions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"current": store.configRevision(), "revisions": store.revisionSummaries()})
	})
	r.Post("/api/config/validate", func(w http.ResponseWriter, r *http.Request) {
		var config GatewayConfig
		if err := decodeJSON(r, &config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid configuration payload"})
			return
		}
		config = normalizeGatewayConfig(config)
		if err := validateGatewayConfig(config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "valid", "config": config})
	})
	r.Put("/api/config", func(w http.ResponseWriter, r *http.Request) {
		var config GatewayConfig
		if err := decodeJSON(r, &config); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid configuration payload"})
			return
		}
		revision, err := applyGatewayConfig(config, "api")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("X-HTTPV-Config-Revision", strconv.FormatUint(revision, 10))
		audit.record(r, "config.apply", strconv.FormatUint(revision, 10), "success")
		writeJSON(w, http.StatusOK, store.config())
	})
	r.Post("/api/config/rollback/{revision}", func(w http.ResponseWriter, r *http.Request) {
		target, err := strconv.ParseUint(chi.URLParam(r, "revision"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid configuration revision"})
			return
		}
		if !requireConfirmation(w, r, "rollback:"+strconv.FormatUint(target, 10)) {
			audit.record(r, "config.rollback", strconv.FormatUint(target, 10), "rejected")
			return
		}
		revision, err := store.rollbackConfig(target)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		if err := persistStore(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to persist rollback"})
			return
		}
		if err := syncGatewayConfig(); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the rollback"})
			return
		}
		w.Header().Set("X-HTTPV-Config-Revision", strconv.FormatUint(revision, 10))
		audit.record(r, "config.rollback", strconv.FormatUint(target, 10), "success")
		writeJSON(w, http.StatusOK, map[string]any{"revision": revision, "config": store.config()})
	})
	r.Get("/api/events", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, store.recentEvents(500)) })
	r.Get("/api/policy-status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, nativePolicy.status())
	})
	demoGenerator := newDemoLoadGenerator(ctx, gatewayURL)
	r.Get("/api/demo/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, demoGenerator.status())
	})
	r.Post("/api/demo/burst", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Count int    `json:"count"`
			Path  string `json:"path"`
		}
		if err := decodeJSON(r, &payload); err != nil || payload.Count < 1 || payload.Count > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "count must be 1-100"})
			return
		}
		if payload.Path == "" {
			payload.Path = "/"
		}
		parsedPath, err := url.ParseRequestURI(payload.Path)
		if err != nil || parsedPath.IsAbs() || parsedPath.Host != "" || !strings.HasPrefix(parsedPath.Path, "/") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path must be a relative request path"})
			return
		}
		accepted := demoGenerator.submit(payload.Count, payload.Path)
		if accepted == 0 {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "demo traffic queue is full"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"accepted": accepted})
	})
	r.Put("/api/subjects/{id}", func(w http.ResponseWriter, r *http.Request) {
		var subject Subject
		if err := decodeJSON(r, &subject); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subject payload"})
			return
		}
		subject.ID = chi.URLParam(r, "id")
		config := store.config()
		found := false
		for index := range config.Subjects {
			if config.Subjects[index].ID == subject.ID {
				config.Subjects[index] = subject
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "subject not found"})
			return
		}
		if _, err := applyGatewayConfig(config, "subject:"+subject.ID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		for _, candidate := range store.config().Subjects {
			if candidate.ID == chi.URLParam(r, "id") {
				subject = candidate
				break
			}
		}
		audit.record(r, "subject.update", subject.ID, "success")
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
		if rule.SubjectID != "" {
			found := false
			for _, subject := range store.config().Subjects {
				if subject.ID == rule.SubjectID {
					found = true
					break
				}
			}
			if !found {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject not found"})
				return
			}
		}
		rule = store.addRule(rule)
		if err := persistStore(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to persist rule"})
			return
		}
		if err := syncGatewayConfig(); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the rule"})
			return
		}
		audit.record(r, "rule.create", rule.ID, "success")
		writeJSON(w, http.StatusCreated, rule)
	})
	r.Delete("/api/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !store.removeRule(chi.URLParam(r, "id")) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
			return
		}
		if err := persistStore(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to persist configuration"})
			return
		}
		if err := syncGatewayConfig(); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the configuration"})
			return
		}
		audit.record(r, "rule.delete", chi.URLParam(r, "id"), "success")
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
		audit.record(r, "pending.decide", chi.URLParam(r, "id")+":"+payload.Action, "success")
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
		audit.record(r, "pending.decide_batch", strconv.Itoa(len(payload.RequestIDs))+":"+payload.Action, "success")
		writeJSON(w, http.StatusOK, map[string]int{"accepted": len(payload.RequestIDs)})
	})
	r.Post("/api/pending/block-all", func(w http.ResponseWriter, r *http.Request) {
		if !requireConfirmation(w, r, "block-all-pending") {
			audit.record(r, "pending.block_all", "", "rejected")
			return
		}
		if err := openresty.blockAllPending(); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenResty has not accepted the emergency action"})
			return
		}
		audit.record(r, "pending.block_all", "", "success")
		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	})

	server := &http.Server{Addr: ":8090", Handler: withCORS(r), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("HTTPV control service listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
