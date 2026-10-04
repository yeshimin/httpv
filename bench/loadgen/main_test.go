package main

import (
	"testing"
	"time"
)

func TestDueRequestsStartsImmediatelyAndMaintainsRate(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		rate    int
		want    int
	}{
		{elapsed: 0, rate: 10, want: 1},
		{elapsed: 99 * time.Millisecond, rate: 10, want: 1},
		{elapsed: 100 * time.Millisecond, rate: 10, want: 2},
		{elapsed: 900 * time.Millisecond, rate: 10, want: 10},
	}
	for _, test := range tests {
		if got := dueRequests(test.elapsed, test.rate); got != test.want {
			t.Fatalf("dueRequests(%s, %d) = %d, want %d", test.elapsed, test.rate, got, test.want)
		}
	}
}
