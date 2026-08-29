//go:build unix

package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadOutcomeSignal1TerminalResultError(t *testing.T) {
	t.Run("antigravity status ERROR", func(t *testing.T) {
		log := `{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}` + "\n" +
			`{"event":"result","result":{"status":"ERROR","num_turns":1,"duration_seconds":1.2}}` + "\n"
		p := writeLog(t, log)
		code, _, reason := readOutcome(p, "antigravity", 0)
		if code != 1 || reason == "" {
			t.Errorf("readOutcome = (code=%d, reason=%q), want (1, non-empty)", code, reason)
		}
	})

	t.Run("claude is_error true", func(t *testing.T) {
		log := `{"type":"result","subtype":"error","is_error":true,"num_turns":1,"total_cost_usd":0.05}` + "\n"
		p := writeLog(t, log)
		code, cost, reason := readOutcome(p, "claude", 0)
		if code != 1 || cost != 0.05 || reason == "" {
			t.Errorf("readOutcome = (code=%d, cost=%v, reason=%q), want (1, 0.05, non-empty)", code, cost, reason)
		}
	})

	t.Run("codex turn.failed", func(t *testing.T) {
		log := `{"type":"turn.failed","error":{"message":"something failed"}}` + "\n"
		p := writeLog(t, log)
		code, _, reason := readOutcome(p, "codex", 0)
		if code != 1 || reason != "something failed" {
			t.Errorf("readOutcome = (code=%d, reason=%q), want (1, %q)", code, reason, "something failed")
		}
	})
}

func TestReadOutcomeSignal2VendorAbortLine(t *testing.T) {
	t.Run("antigravity stderr permission abort line", func(t *testing.T) {
		log := `{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}` + "\n" +
			`{"event":"step_update","step_update":{"state":"DONE","step_type":"tool","tool_name":"view_file","tool_info":{"output":"ok"}}}` + "\n" +
			`jetski: no output produced — a tool required the "command" permission that` + "\n" +
			`headless mode cannot prompt for, so it was auto-denied.` + "\n" +
			`{"event":"result","result":{"status":"SUCCESS","response":"","num_turns":1}}` + "\n"
		p := writeLog(t, log)
		code, _, reason := readOutcome(p, "antigravity", 0)
		if code != 1 {
			t.Errorf("readOutcome code = %d, want 1", code)
		}
		if !strings.Contains(reason, "--dangerously-skip-permissions") {
			t.Errorf("readOutcome reason = %q, want remedy in note", reason)
		}
	})
}

func TestReadOutcomeSignal3EmptyStream(t *testing.T) {
	t.Run("decoded end to end with no prose and no successful tool", func(t *testing.T) {
		log := `{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}` + "\n" +
			`{"event":"step_update","step_update":{"state":"ERROR","step_type":"tool","tool_name":"run_command","tool_info":{"error":{"type":"TOOL_ERROR","message":"denied"}}}}` + "\n" +
			`{"event":"result","result":{"status":"SUCCESS","response":"","num_turns":1}}` + "\n"
		logPath := writeLog(t, log)
		code, _, reason := readOutcome(logPath, "unknown_vendor", 0)
		if code != 1 || reason != "run produced no output" {
			t.Errorf("readOutcome = (code=%d, reason=%q), want (1, %q)", code, reason, "run produced no output")
		}
	})
}

func TestReadOutcomeSuccessfulRunsNotOverturned(t *testing.T) {
	t.Run("antigravity with response text", func(t *testing.T) {
		log := `{"event":"init","conversation_id":"ffd2f49a-85bf-45ab-bfad-80aed96a9b98"}` + "\n" +
			`{"event":"step_update","step_update":{"state":"DONE","step_type":"agent_response","text_delta":"Done."}}` + "\n" +
			`{"event":"result","result":{"status":"SUCCESS","response":"Done.","num_turns":1}}` + "\n"
		p := writeLog(t, log)
		code, _, reason := readOutcome(p, "antigravity", 0)
		if code != 0 || reason != "" {
			t.Errorf("readOutcome = (code=%d, reason=%q), want (0, empty)", code, reason)
		}
	})

	t.Run("antigravity without response field but with tool success", func(t *testing.T) {
		log := `{"event":"init","conversation_id":"c50b4e3f-6b7d-4521-8821-5558448eda5e"}` + "\n" +
			`{"event":"step_update","step_update":{"state":"DONE","step_type":"tool","tool_name":"view_file","tool_info":{"output":"2 lines"}}}` + "\n" +
			`{"event":"result","result":{"status":"SUCCESS","num_turns":1}}` + "\n"
		p := writeLog(t, log)
		code, _, reason := readOutcome(p, "antigravity", 0)
		if code != 0 || reason != "" {
			t.Errorf("readOutcome = (code=%d, reason=%q), want (0, empty)", code, reason)
		}
	})

	t.Run("raw undecodable log", func(t *testing.T) {
		log := "Just some plain text from a simple script\nAll good\n"
		p := writeLog(t, log)
		code, _, reason := readOutcome(p, "unknown", 0)
		if code != 0 || reason != "" {
			t.Errorf("readOutcome = (code=%d, reason=%q), want (0, empty)", code, reason)
		}
	})
}

func TestReadOutcomeNonZeroExitCodeUnchanged(t *testing.T) {
	log := `{"type":"result","subtype":"success","num_turns":1}` + "\n"
	p := writeLog(t, log)
	code, _, reason := readOutcome(p, "claude", 3)
	if code != 3 || reason != "" {
		t.Errorf("readOutcome = (code=%d, reason=%q), want (3, empty)", code, reason)
	}
}
