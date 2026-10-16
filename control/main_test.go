package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func frameText(value string) []byte {
	result := make([]byte, 2+len(value))
	binary.BigEndian.PutUint16(result[:2], uint16(len(value)))
	copy(result[2:], value)
	return result
}

func testFrame() []byte {
	frame := make([]byte, 38)
	copy(frame[:4], "HTVP")
	frame[4] = 1
	frame[5] = 7
	binary.BigEndian.PutUint64(frame[8:16], 1234)
	binary.BigEndian.PutUint64(frame[16:24], 12)
	binary.BigEndian.PutUint64(frame[24:32], 34)
	binary.BigEndian.PutUint16(frame[32:34], 200)
	binary.BigEndian.PutUint32(frame[34:38], 56)
	for _, value := range []string{"request-1", "subject-1", "GET", "/demo", "127.0.0.1", ""} {
		frame = append(frame, frameText(value)...)
	}
	return frame
}

func TestDecodeHTVP(t *testing.T) {
	event, ok := decodeHTVP(testFrame())
	if !ok {
		t.Fatal("expected valid frame")
	}
	if event.Phase != "response_finished" || event.RequestID != "request-1" || event.SubjectID != "subject-1" {
		t.Fatalf("unexpected event identity: %#v", event)
	}
	if event.TimestampMS != 1234 || event.RequestBytes != 12 || event.ResponseBytes != 34 || event.Status != 200 || event.DurationMS != 56 {
		t.Fatalf("unexpected event metrics: %#v", event)
	}
}

func TestRecentEventsUsesFixedRing(t *testing.T) {
	store := newStore()
	for i := 0; i < maxRecentEvents+2; i++ {
		store.addEvent(TrafficEvent{RequestID: string(rune(i + 1))})
	}
	recent := store.recentEvents(3)
	if len(recent) != 3 {
		t.Fatalf("got %d events, want 3", len(recent))
	}
	if recent[0].RequestID != string(rune(maxRecentEvents)) || recent[2].RequestID != string(rune(maxRecentEvents+2)) {
		t.Fatalf("ring order is wrong: %#v", recent)
	}
}

func TestRecentEventsIsEmptyBeforeTrafficArrives(t *testing.T) {
	recent := newStore().recentEvents(500)
	if len(recent) != 0 {
		t.Fatalf("got %d events, want none", len(recent))
	}
}

func TestRequestRankingsSortByRequestStarts(t *testing.T) {
	store := newStore()
	for index := 0; index < 3; index++ {
		store.addEvent(TrafficEvent{Phase: "request_started", Method: "POST", Path: "/orders"})
		store.addEvent(TrafficEvent{Phase: "response_finished", Method: "POST", Path: "/orders"})
	}
	store.addEvent(TrafficEvent{Phase: "request_started", Method: "GET", Path: "/products"})
	rankings := store.requestRankings(0, 20)
	if len(rankings) != 2 {
		t.Fatalf("got %d rankings, want 2", len(rankings))
	}
	if rankings[0].Method != "POST" || rankings[0].Path != "/orders" || rankings[0].Count != 3 {
		t.Fatalf("unexpected leading ranking: %#v", rankings[0])
	}
	if rankings[1].Method != "GET" || rankings[1].Path != "/products" || rankings[1].Count != 1 {
		t.Fatalf("unexpected second ranking: %#v", rankings[1])
	}
}

func TestRequestRankingsRetainMoreThanDisplayLimit(t *testing.T) {
	store := newStore()
	const total = 5_000
	for index := 0; index < total; index++ {
		store.addEvent(TrafficEvent{Phase: "request_started", Method: "GET", Path: fmt.Sprintf("/catalog/%d", index)})
	}
	if got := store.requestRankingTotal(); got != total {
		t.Fatalf("tracked %d request paths, want %d", got, total)
	}
	if got := len(store.requestRankings(0, 20)); got != 20 {
		t.Fatalf("displayed %d rankings, want 20", got)
	}
	if got := len(store.requestRankings(0, -1)); got != total {
		t.Fatalf("returned %d rankings for unlimited query, want %d", got, total)
	}
	if got := len(store.requestRankings(4_950, 50)); got != 50 {
		t.Fatalf("returned %d rankings for final page, want 50", got)
	}
	if got := len(store.requestRankings(total, 50)); got != 0 {
		t.Fatalf("returned %d rankings past the final page, want 0", got)
	}
}

func TestClearRequestRankings(t *testing.T) {
	store := newStore()
	store.addEvent(TrafficEvent{Phase: "request_started", Method: "GET", Path: "/products"})
	store.addEvent(TrafficEvent{Phase: "request_started", Method: "POST", Path: "/orders"})
	if cleared := store.clearRequestRankings(); cleared != 2 {
		t.Fatalf("cleared %d rankings, want 2", cleared)
	}
	if total := store.requestRankingTotal(); total != 0 {
		t.Fatalf("ranking total is %d after clear, want 0", total)
	}
	if rankings := store.requestRankings(0, 20); len(rankings) != 0 {
		t.Fatalf("got rankings after clear: %#v", rankings)
	}
}

func TestTrafficSummaryKeepsTopPathsAndControlSignals(t *testing.T) {
	accumulator := &trafficSummaryAccumulator{paths: map[string]*TrafficPathSummary{}}
	accumulator.add(TrafficEvent{Phase: "request_started", TimestampMS: 100, SubjectID: "demo", Method: "GET", Path: "/hot"})
	accumulator.add(TrafficEvent{Phase: "request_pending", TimestampMS: 120, SubjectID: "demo", Method: "GET", Path: "/hot"})
	accumulator.add(TrafficEvent{Phase: "request_blocked", TimestampMS: 140, SubjectID: "demo", Method: "GET", Path: "/hot"})
	accumulator.add(TrafficEvent{Phase: "response_finished", TimestampMS: 160, SubjectID: "demo", Method: "GET", Path: "/cold"})

	summary := accumulator.take(600)
	if summary == nil || summary.Total != 4 || summary.Started != 1 || summary.Finished != 1 || summary.Pending != 1 || summary.Blocked != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.Paths) != 2 || summary.Paths[0].Path != "/hot" || summary.Paths[0].Count != 3 || summary.Paths[0].Pending != 1 || summary.Paths[0].Blocked != 1 {
		t.Fatalf("unexpected path aggregation: %#v", summary.Paths)
	}
}

func TestDetailBudgetPreservesVisibleAndPriorityRequests(t *testing.T) {
	const detailRate = 8
	now := time.Now()
	c := &client{visible: map[string]time.Time{}}
	first := TrafficEvent{Phase: "request_started", RequestID: "first"}
	if !c.shouldShowDetail(first, now, detailRate) || !c.shouldShowDetail(TrafficEvent{Phase: "response_finished", RequestID: "first"}, now, detailRate) {
		t.Fatal("visible request lifecycle should remain detailed")
	}
	for i := 0; i < detailRate-1; i++ {
		if !c.shouldShowDetail(TrafficEvent{Phase: "request_started", RequestID: string(rune(i + 1_000))}, now, detailRate) {
			t.Fatal("detail budget ended too early")
		}
	}
	if c.shouldShowDetail(TrafficEvent{Phase: "request_started", RequestID: "summarized"}, now, detailRate) {
		t.Fatal("ordinary request should become summarized after the detail budget")
	}
	if !c.shouldShowDetail(TrafficEvent{Phase: "request_pending", RequestID: "priority"}, now, detailRate) {
		t.Fatal("manual-gate request must remain detailed under overload")
	}
}

func TestHubSendsOverloadSummaryAfterDetailBudget(t *testing.T) {
	const detailRate = 8
	hub := newHub()
	hub.detailRate = detailRate
	c := &client{
		priority: make(chan []byte, 4),
		detail:   make(chan []byte, detailRate+1),
		visible:  map[string]time.Time{},
	}
	hub.clients[c] = struct{}{}
	for i := 0; i < detailRate+1; i++ {
		hub.broadcast(TrafficEvent{
			Phase:       "request_started",
			RequestID:   string(rune(i + 2_000)),
			SubjectID:   "demo",
			Method:      "GET",
			Path:        "/overflow",
			TimestampMS: int64(1_000 + i),
		})
	}
	hub.flushSummaries(time.Now())
	select {
	case message := <-c.priority:
		var summary TrafficSummary
		if err := json.Unmarshal(message, &summary); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		if summary.Type != "traffic_summary" || summary.Total != 1 || len(summary.Paths) != 1 || summary.Paths[0].Path != "/overflow" {
			t.Fatalf("unexpected overload summary: %#v", summary)
		}
	default:
		t.Fatal("expected an overload summary")
	}
}

func TestHubReadsVisualDetailRate(t *testing.T) {
	t.Setenv("HTTPV_VISUAL_DETAIL_STARTS_PER_SEC", "27")
	if rate := newHub().detailRate; rate != 27 {
		t.Fatalf("expected configured detail rate 27, got %d", rate)
	}
}

func TestDemoLoadGeneratorReportsActiveAndIdle(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	generator := newDemoLoadGenerator(ctx, server.URL)
	if maximum := generator.status()["max_concurrency"]; maximum != 0 {
		t.Fatalf("expected unbounded concurrency marker 0, got %d", maximum)
	}
	if accepted := generator.submit(1, "/probe"); accepted != 1 {
		t.Fatalf("expected one accepted job, got %d", accepted)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("demo request did not start")
	}
	status := generator.status()
	if status["active"] != 1 || status["queued"] != 0 {
		t.Fatalf("unexpected active status: %#v", status)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status = generator.status()
		if status["active"] == 0 && status["queued"] == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("demo generator did not become idle: %#v", status)
}

func TestTerminalEventsEndBrowserLifecycle(t *testing.T) {
	if terminalEvent(TrafficEvent{Phase: "request_blocked"}) {
		t.Fatal("block event must retain the lifecycle for response events")
	}
	if !terminalEvent(TrafficEvent{Phase: "response_finished"}) {
		t.Fatal("response finish must close the browser lifecycle")
	}
	if !terminalEvent(TrafficEvent{Phase: "request_aborted"}) {
		t.Fatal("client abort must close the browser lifecycle")
	}
}

func TestDecodeRequestAbortedPhase(t *testing.T) {
	phase, ok := phaseName(8)
	if !ok || phase != "request_aborted" {
		t.Fatalf("phase 8 = %q, %v; want request_aborted", phase, ok)
	}
	if !priorityEvent(TrafficEvent{Phase: phase}) {
		t.Fatal("client abort must bypass visualization sampling")
	}
}

func TestDecodeRequestOverflowPhase(t *testing.T) {
	phase, ok := phaseName(9)
	if !ok || phase != "request_overflow" {
		t.Fatalf("phase 9 = %q, %v; want request_overflow", phase, ok)
	}
	if !priorityEvent(TrafficEvent{Phase: phase}) {
		t.Fatal("gate overflow must bypass visualization sampling")
	}
}

func TestEncodeNativePolicy(t *testing.T) {
	policy, err := encodeNativePolicy(GatewayConfig{
		Subjects: []Subject{{
			ID: "subject-a", Enabled: true, Display: true,
			Match: Match{Host: "api.local", PathPrefix: "/v1", Method: "GET"},
			Gate:  Gate{Enabled: false},
		}},
		Rules: []Rule{{Action: "block", Method: "POST", PathPrefix: "/admin", ClientIP: "10.0.0.8"}},
	})
	if err != nil {
		t.Fatalf("encode native policy: %v", err)
	}
	if string(policy[:4]) != "HTVC" || policy[4] != nativePolicyVersion || policy[5] != 1 || policy[6] != 1 || policy[7] != 0 || binary.BigEndian.Uint16(policy[8:10]) != 1 || binary.BigEndian.Uint64(policy[10:18]) != 1 {
		t.Fatalf("unexpected policy header: %v", policy[:nativePolicyHeaderLen])
	}
	offset := nativePolicyHeaderLen
	read := func() string {
		length := int(binary.BigEndian.Uint16(policy[offset : offset+2]))
		offset += 2
		value := string(policy[offset : offset+length])
		offset += length
		return value
	}
	values := []string{read(), read(), read(), read(), read(), read(), read()}
	want := []string{"subject-a", "api.local", "/v1", "GET", "POST", "/admin", "10.0.0.8"}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("field %d = %q, want %q", i, values[i], want[i])
		}
	}
}

func TestNativePolicyVersionAndAckStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.bin")
	publisher := newNativePolicyPublisher(path)
	config := GatewayConfig{Subjects: []Subject{{ID: "demo", Enabled: true, Display: true, Match: Match{PathPrefix: "/"}}}}
	first, err := publisher.publish(config)
	if err != nil || first != 1 {
		t.Fatalf("first publish = %d, %v", first, err)
	}
	unchanged, err := publisher.publish(config)
	if err != nil || unchanged != first {
		t.Fatalf("unchanged publish = %d, %v", unchanged, err)
	}
	config.Subjects[0].Match.PathPrefix = "/v2"
	second, err := publisher.publish(config)
	if err != nil || second != 2 {
		t.Fatalf("changed publish = %d, %v", second, err)
	}
	staleAck := path + ".ack.stale"
	if err := os.WriteFile(staleAck, []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restarted := newNativePolicyPublisher(path)
	third, err := restarted.publish(config)
	if err != nil || third != 3 {
		t.Fatalf("restart publish = %d, %v", third, err)
	}
	if _, err := os.Stat(staleAck); !os.IsNotExist(err) {
		t.Fatalf("stale worker ack was not removed: %v", err)
	}
	if err := os.WriteFile(path+".ack.42", []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := restarted.status()
	if status.State != "active" || status.Mode != "native" || status.Version != 3 || len(status.Acks) != 1 || status.Acks[0].Worker != "42" || status.Acks[0].Version != 3 {
		t.Fatalf("unexpected policy status: %#v", status)
	}
}

func TestControlStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	store := newStore()
	store.updateSubject(Subject{
		ID: "default", Name: "Persistent API", Enabled: true, Display: true,
		Match: Match{PathPrefix: "/persistent"},
		Gate:  Gate{Enabled: true, TimeoutMS: 4_000, TimeoutAction: "allow"},
	})
	rule := store.addRule(Rule{PathPrefix: "/blocked", Method: "POST"})
	if err := store.persist(path); err != nil {
		t.Fatal(err)
	}
	restored, err := loadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	config := restored.config()
	if len(config.Subjects) != 1 || config.Subjects[0].Name != "Persistent API" || !config.Subjects[0].Gate.Enabled || config.Subjects[0].Gate.TimeoutAction != "allow" {
		t.Fatalf("subject was not restored: %#v", config.Subjects)
	}
	if len(config.Rules) != 1 || config.Rules[0].ID != rule.ID || config.Rules[0].PathPrefix != "/blocked" {
		t.Fatalf("rules were not restored: %#v", config.Rules)
	}
}

func TestGatewayConfigValidatesRouteReferences(t *testing.T) {
	config := defaultGatewayConfig()
	config.Routes[0].UpstreamID = "missing"
	if err := validateGatewayConfig(normalizeGatewayConfig(config)); err == nil {
		t.Fatal("expected an unknown upstream validation error")
	}
	config.Routes[0].UpstreamID = config.Upstreams[0].ID
	config.Upstreams = append(config.Upstreams, Upstream{ID: "second", URL: "http://app:8080"})
	config.Routes = append(config.Routes, Route{ID: "api", Enabled: true, Match: Match{Host: "api.local", PathPrefix: "/v1"}, UpstreamID: "second"})
	if err := validateGatewayConfig(normalizeGatewayConfig(config)); err != nil {
		t.Fatalf("valid multi-route configuration rejected: %v", err)
	}
}

func TestGatewayConfigSchemaAndTargets(t *testing.T) {
	config := defaultGatewayConfig()
	config.SchemaVersion = 2
	if err := validateGatewayConfig(normalizeGatewayConfig(config)); err == nil {
		t.Fatal("expected unsupported schema version error")
	}
	config = defaultGatewayConfig()
	config.Upstreams[0].Targets[0].Weight = 0
	normalized := normalizeGatewayConfig(config)
	if normalized.Upstreams[0].Targets[0].Weight != 1 {
		t.Fatalf("target weight was not normalized: %#v", normalized.Upstreams[0])
	}
	cloned := cloneGatewayConfig(normalized)
	cloned.Upstreams[0].Targets[0].URL = "http://changed:8080"
	cloned.Subjects[0].Capture.RedactHeaders[0] = "changed"
	if normalized.Upstreams[0].Targets[0].URL == cloned.Upstreams[0].Targets[0].URL || normalized.Subjects[0].Capture.RedactHeaders[0] == "changed" {
		t.Fatal("gateway clone shares nested configuration slices")
	}
}

func TestGatewayConfigValidatesGateProtection(t *testing.T) {
	config := defaultGatewayConfig()
	config.Subjects[0].Gate.MaxPending = -1
	if err := validateGatewayConfig(normalizeGatewayConfig(config)); err == nil {
		t.Fatal("expected a negative pending limit validation error")
	}
	config = defaultGatewayConfig()
	config.Subjects[0].Gate.OverflowAction = "invalid"
	normalized := normalizeGatewayConfig(config)
	if normalized.Subjects[0].Gate.OverflowAction != "block" {
		t.Fatalf("invalid overflow action was not normalized: %#v", normalized.Subjects[0].Gate)
	}
}

func TestGatewayConfigValidatesCapturePolicy(t *testing.T) {
	config := defaultGatewayConfig()
	config.Subjects[0].Capture.Enabled = true
	config.Subjects[0].Capture.MaxBodyBytes = (1 << 20) + 1
	if err := validateGatewayConfig(normalizeGatewayConfig(config)); err == nil {
		t.Fatal("expected an oversized capture body limit validation error")
	}
	config = defaultGatewayConfig()
	config.Subjects[0].Capture = CapturePolicy{}
	normalized := normalizeGatewayConfig(config)
	if normalized.Subjects[0].Capture.MaxBodyBytes != 16*1024 || !normalized.Subjects[0].Capture.RequestHeaders || !normalized.Subjects[0].Capture.ResponseHeaders {
		t.Fatalf("capture defaults were not applied: %#v", normalized.Subjects[0].Capture)
	}
}

func TestNativePolicyDisablesMultiSubjectConfiguration(t *testing.T) {
	config := defaultGatewayConfig()
	config.Subjects = append(config.Subjects, Subject{ID: "second", Enabled: true, Display: true, Match: Match{PathPrefix: "/second"}, Gate: Gate{TimeoutAction: "block"}})
	policy, err := encodeNativePolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	if policy[5] != 0 || binary.BigEndian.Uint16(policy[8:10]) != 0 {
		t.Fatalf("multi-subject policy must fall back to Lua: header=%v", policy[:nativePolicyHeaderLen])
	}
}

func TestNativePolicyFallsBackWhenCaptureIsEnabled(t *testing.T) {
	config := defaultGatewayConfig()
	config.Subjects[0].Capture.Enabled = true
	policy, err := encodeNativePolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	if policy[5] != 0 {
		t.Fatalf("capture-enabled policy must fall back to Lua: header=%v", policy[:nativePolicyHeaderLen])
	}
}

func TestPersistedStateRecoversWhenInitialConfigIsMissing(t *testing.T) {
	directory := t.TempDir()
	statePath := filepath.Join(directory, "control-state.json")
	store := newStore()
	store.updateSubject(Subject{ID: "default", Name: "Recovered", Enabled: true, Display: true, Match: Match{PathPrefix: "/"}, Gate: Gate{TimeoutAction: "block"}})
	if err := store.persist(statePath); err != nil {
		t.Fatal(err)
	}
	restored, err := loadStore(statePath, filepath.Join(directory, "missing.json"))
	if err != nil {
		t.Fatalf("persisted state should recover without the initial file: %v", err)
	}
	if restored.config().Subjects[0].Name != "Recovered" {
		t.Fatalf("unexpected restored config: %#v", restored.config())
	}
}

func TestConfigurationRevisionRollback(t *testing.T) {
	store := newStore()
	base := store.config()
	changed := cloneGatewayConfig(base)
	changed.Subjects[0].Name = "Changed"
	changedRevision, err := store.applyConfig(changed, "test")
	if err != nil || changedRevision != 2 {
		t.Fatalf("apply revision = %d, %v", changedRevision, err)
	}
	rollbackRevision, err := store.rollbackConfig(1)
	if err != nil || rollbackRevision != 3 {
		t.Fatalf("rollback revision = %d, %v", rollbackRevision, err)
	}
	if store.config().Subjects[0].Name != base.Subjects[0].Name {
		t.Fatalf("rollback did not restore base config: %#v", store.config())
	}
	history := store.revisionSummaries()
	if len(history) != 3 || history[0].Revision != 3 || history[0].Source != "rollback:1" {
		t.Fatalf("unexpected revision history: %#v", history)
	}
}
