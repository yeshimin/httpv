package main

import (
	"encoding/binary"
	"testing"
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
