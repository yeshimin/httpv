package main

import (
	"encoding/binary"
	"encoding/json"
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
	now := time.Now()
	c := &client{visible: map[string]time.Time{}}
	first := TrafficEvent{Phase: "request_started", RequestID: "first"}
	if !c.shouldShowDetail(first, now) || !c.shouldShowDetail(TrafficEvent{Phase: "response_finished", RequestID: "first"}, now) {
		t.Fatal("visible request lifecycle should remain detailed")
	}
	for i := 0; i < visualDetailStartsPerSec-1; i++ {
		if !c.shouldShowDetail(TrafficEvent{Phase: "request_started", RequestID: string(rune(i + 1_000))}, now) {
			t.Fatal("detail budget ended too early")
		}
	}
	if c.shouldShowDetail(TrafficEvent{Phase: "request_started", RequestID: "summarized"}, now) {
		t.Fatal("ordinary request should become summarized after the detail budget")
	}
	if !c.shouldShowDetail(TrafficEvent{Phase: "request_pending", RequestID: "priority"}, now) {
		t.Fatal("manual-gate request must remain detailed under overload")
	}
}

func TestHubSendsOverloadSummaryAfterDetailBudget(t *testing.T) {
	hub := newHub()
	c := &client{
		priority: make(chan []byte, 4),
		detail:   make(chan []byte, visualDetailStartsPerSec+1),
		visible:  map[string]time.Time{},
	}
	hub.clients[c] = struct{}{}
	for i := 0; i < visualDetailStartsPerSec+1; i++ {
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
