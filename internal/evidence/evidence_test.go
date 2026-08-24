package evidence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordsRoundTripAndRemove(t *testing.T) {
	dir := t.TempDir()
	e := newEvidence(dir, nil)
	want := Record{
		Lane:           2,
		PID:            42,
		ProcessStarted: "Sat Aug 23 12:00:00 2026",
		StartedAt:      time.Date(2026, 8, 23, 12, 1, 0, 0, time.UTC),
		TicketID:       "LERP-8",
		Queue:          "Implementing",
		StartingStatus: "Todo",
		Workspace:      "/tmp/lerp-8",
		LogPath:        "/tmp/lerp-8.log",
		SessionID:      "session",
	}
	got, err := e.Create(want)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID == "" || got.LogPath == "" {
		t.Fatalf("Create did not set run ID and log path: %#v", got)
	}
	if _, err := os.Stat(got.LogPath); err != nil {
		t.Fatalf("run log: %v", err)
	}
	got, err = e.Read(got.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TicketID != want.TicketID || got.Lane != want.Lane || got.RunID == "" {
		t.Errorf("record = %#v, want the original run facts", got)
	}
	if err := e.Remove(got.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Read(got.RunID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read after Remove error = %v, want not exist", err)
	}
	if err := e.Remove(got.RunID); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
}

func TestWriteRejectsInvalidLane(t *testing.T) {
	e := newEvidence(t.TempDir(), nil)
	if _, err := e.Create(Record{}); err == nil {
		t.Error("Create succeeded for lane zero")
	}
}

func TestList(t *testing.T) {
	e := newEvidence(t.TempDir(), nil)
	first, err := e.Create(Record{Lane: 1, TicketID: "LERP-8"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Create(Record{Lane: 2, TicketID: "LERP-9"})
	if err != nil {
		t.Fatal(err)
	}
	records, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("List returned %d records, want 2", len(records))
	}
	seen := map[string]bool{records[0].RunID: true, records[1].RunID: true}
	if !seen[first.RunID] || !seen[second.RunID] {
		t.Errorf("List = %#v, want both created runs", records)
	}
}

func TestAcquireLock(t *testing.T) {
	identities := map[int]struct {
		start string
		alive bool
	}{200: {"old-start", true}, 300: {"new-start", true}}
	e := newEvidence(t.TempDir(), func(pid int) (string, bool) {
		if pid == os.Getpid() {
			return "self-start", true
		}
		v := identities[pid]
		return v.start, v.alive
	})

	// The initial lock is written directly so it can model another process.
	other := lockRecord{PID: 200, ProcessStarted: "old-start", StartedAt: time.Now().UTC()}
	if err := os.MkdirAll(e.statePath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := createJSON(e.lockPath(), other); err != nil {
		t.Fatal(err)
	}
	_, err := e.AcquireLock()
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("AcquireLock error = %v, want ErrLocked", err)
	}
}

func TestAcquireLockBreaksDeadAndReusedPID(t *testing.T) {
	for _, tt := range []struct {
		name     string
		stored   lockRecord
		identity func(int) (string, bool)
	}{
		{
			name:   "dead process",
			stored: lockRecord{PID: 200, ProcessStarted: "old"},
			identity: func(pid int) (string, bool) {
				if pid == os.Getpid() {
					return "self", true
				}
				return "", false
			},
		},
		{
			name:   "reused pid",
			stored: lockRecord{PID: 200, ProcessStarted: "old"},
			identity: func(pid int) (string, bool) {
				if pid == os.Getpid() {
					return "self", true
				}
				return "new", true
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEvidence(t.TempDir(), tt.identity)
			if err := os.MkdirAll(e.statePath(), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := createJSON(e.lockPath(), tt.stored); err != nil {
				t.Fatal(err)
			}
			lock, err := e.AcquireLock()
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLockCloseWillNotRemoveReplacement(t *testing.T) {
	e := newEvidence(t.TempDir(), processStart)
	lock, err := e.AcquireLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.lockPath()); err != nil {
		t.Fatal(err)
	}
	replacement := lockRecord{PID: 999, ProcessStarted: "replacement", StartedAt: time.Now().UTC()}
	if err := createJSON(e.lockPath(), replacement); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err == nil {
		t.Fatal("Close succeeded after lock was replaced")
	}
	if _, err := os.Stat(filepath.Join(e.statePath(), lockFile)); err != nil {
		t.Fatalf("replacement lock missing: %v", err)
	}
}
