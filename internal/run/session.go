//go:build unix

package run

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/mattwalters/lerp/internal/config"
	"github.com/mattwalters/lerp/internal/evidence"
	"github.com/mattwalters/lerp/internal/vendors"
)

// sessionScanBudget bounds how many lines resolveSession reads from a run's
// log looking for the session a SessionNamer vendor announced. It is
// legible rather than defensive: the session-naming event is the stream's
// first, and logfmt's sniffWindow already establishes that a couple of
// lines can precede it on the same file descriptor.
const sessionScanBudget = 64

// CapturesSession reports whether runner's vendor names its own session
// instead of accepting one lerp mints — the other way a run can be
// resumable, beside OpensSession.
func CapturesSession(runner config.Runner) bool {
	_, ok := sessionNamerFor(runner)
	return ok
}

// resolveSession returns the session id record's run should resume with:
// record.SessionID directly for a runner that minted one, or a bounded scan
// of the run's own log for a runner whose vendor names its own session.
// Neither source having one — a run that died before ever reporting a
// session — is an error, named plainly rather than left to surface as an
// empty id in a resume command that cannot be used.
func resolveSession(runner config.Runner, record evidence.Record) (string, error) {
	if record.SessionID != "" {
		return record.SessionID, nil
	}
	namer, ok := sessionNamerFor(runner)
	if !ok {
		return "", nil
	}
	if id := scanForSession(record.LogPath, namer); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("run %s never reported a session id, so there is nothing to resume", record.RunID)
}

// sessionNamerFor is the vendors.SessionNamer runner's vendor implements, if
// any: unset for a hand-written command runner, and for a vendor whose CLI
// accepts a session lerp mints instead of naming its own.
func sessionNamerFor(runner config.Runner) (vendors.SessionNamer, bool) {
	adapter, ok := vendors.Lookup(runner.Vendor)
	if !ok {
		return nil, false
	}
	namer, ok := adapter.(vendors.SessionNamer)
	return namer, ok
}

// scanForSession reads logPath line by line, asking namer of each, and
// returns the first session id it names — or "" when the file cannot be
// opened, the budget runs out, or the file ends first, all of which read the
// same to the caller: this run's log never said. A bufio.Reader rather than
// a Scanner, since an agent log can put a whole tool result on one line,
// well past Scanner's default token size.
func scanForSession(logPath string, namer vendors.SessionNamer) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for range sessionScanBudget {
		line, err := r.ReadString('\n')
		if id, ok := namer.Session(strings.TrimSuffix(line, "\n")); ok {
			return id
		}
		if err != nil {
			return ""
		}
	}
	return ""
}
