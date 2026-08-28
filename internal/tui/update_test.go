package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/mattwalters/lerp/internal/loop"
	updatepkg "github.com/mattwalters/lerp/internal/update"
	"github.com/mattwalters/lerp/internal/version"
)

func TestUpdateNoticeAndAnnounce(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)

	// An update message with Announce=true sets a transient note on the status bar.
	msg := updateMsg{notice: updatepkg.Notice{
		Latest:   "v0.2.0",
		URL:      "https://github.com/mattwalters/lerp/releases/tag/v0.2.0",
		Announce: true,
	}}
	m = update(t, m, msg)
	if m.updateNotice.Latest != "v0.2.0" {
		t.Fatalf("m.updateNotice.Latest = %q, want v0.2.0", m.updateNotice.Latest)
	}
	if len(m.notes) == 0 {
		t.Fatalf("expected note on status bar after Announce=true, got none")
	}
	if want := "lerp v0.2.0 is available · u to upgrade"; !strings.Contains(m.notes[0].text, want) {
		t.Errorf("note = %q, want containing %q", m.notes[0].text, want)
	}

	// An update message with Announce=false records the notice but sets no note.
	m2, _, _ := newTestModel(t, 1)
	m2 = pastTheSplash(t, m2)
	msgNoAnnounce := updateMsg{notice: updatepkg.Notice{
		Latest:   "v0.2.0",
		URL:      "https://github.com/mattwalters/lerp/releases/tag/v0.2.0",
		Announce: false,
	}}
	m2 = update(t, m2, msgNoAnnounce)
	if m2.updateNotice.Latest != "v0.2.0" {
		t.Fatalf("m2.updateNotice.Latest = %q, want v0.2.0", m2.updateNotice.Latest)
	}
	if len(m2.notes) != 0 {
		t.Errorf("expected no note on status bar after Announce=false, got: %+v", m2.notes)
	}
}

func TestUpgradeModalWorkflow(t *testing.T) {
	origVer := version.Version
	version.Version = "v0.1.0"
	defer func() { version.Version = origVer }()

	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)

	// Pressing 'u' with no update is inert.
	m = update(t, m, keyMsg("u"))
	if m.upgradeOn {
		t.Fatalf("u opened upgrade modal when no update was available")
	}

	// Receive update notice
	m = update(t, m, updateMsg{notice: updatepkg.Notice{
		Latest:   "v0.2.0",
		URL:      "https://github.com/mattwalters/lerp/releases/tag/v0.2.0",
		Announce: true,
	}})

	// Pressing 'u' opens the upgrade modal
	m = update(t, m, keyMsg("u"))
	if !m.upgradeOn {
		t.Fatalf("u failed to open upgrade modal")
	}

	view := m.View()
	for _, want := range []string{
		"current v0.1.0 · latest v0.2.0",
		"brew upgrade lerp",
		"go install github.com/mattwalters/lerp/cmd/lerp@latest",
		"https://github.com/mattwalters/lerp/releases/tag/v0.2.0",
		"esc dismisses this panel",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("upgrade modal view missing %q:\n%s", want, view)
		}
	}

	// Pressing 'esc' dismisses the modal
	m = update(t, m, keyMsg("esc"))
	if m.upgradeOn {
		t.Fatalf("esc failed to dismiss upgrade modal")
	}
}

func TestHelpShowsUpgradeKeyOnlyWhenUpdateAvailable(t *testing.T) {
	m, _, _ := newTestModel(t, 1)
	m = pastTheSplash(t, m)

	// Without update: ? help does not list upgrade
	helpTextWithoutUpdate := m.helpText()
	if strings.Contains(helpTextWithoutUpdate, "u to upgrade") || strings.Contains(helpTextWithoutUpdate, "upgrade") {
		t.Errorf("helpText without update contains upgrade hints:\n%s", helpTextWithoutUpdate)
	}

	// With update: ? help lists upgrade
	m = update(t, m, updateMsg{notice: updatepkg.Notice{
		Latest:   "v0.2.0",
		URL:      "https://github.com/mattwalters/lerp/releases/tag/v0.2.0",
		Announce: false,
	}})

	helpTextWithUpdate := m.helpText()
	if !strings.Contains(helpTextWithUpdate, "u") || !strings.Contains(helpTextWithUpdate, "upgrade") {
		t.Errorf("helpText with update missing upgrade hints:\n%s", helpTextWithUpdate)
	}
}

func TestNilCheckUpdateProducesNoNotice(t *testing.T) {
	ticker := &countingTicker{}
	events := make(chan loop.Event, 8)
	m := newModel(context.Background(), Options{
		Engine:      fakeEngine{ticker, &recordingPromoter{}, &recordingEjector{}, &recordingStarter{}, &recordingReader{}},
		Statuses:    defaultTestStatuses,
		Interval:    1000,
		Lanes:       1,
		Events:      events,
		CheckUpdate: nil,
	})
	m = resize(t, m, 100, 30)
	m = pastTheSplash(t, m)

	if m.updateNotice.Latest != "" {
		t.Errorf("m.updateNotice = %+v, want empty", m.updateNotice)
	}
	if len(m.notes) > 0 {
		t.Errorf("unexpected notes: %+v", m.notes)
	}
}
