package main

import (
	"strings"
	"testing"
)

// The operator's surface says concurrency, never lane. Lane stays the
// internal noun and the evidence record's field; two names for one number
// is exactly the clutter this project refuses.
func TestUsageDoesNotSayLane(t *testing.T) {
	if strings.Contains(strings.ToLower(usage), "lane") {
		t.Fatalf("usage names a lane:\n%s", usage)
	}
}
