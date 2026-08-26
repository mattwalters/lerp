package workspace

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/mattwalters/lerp/internal/childenv"
)

// Environment variable names passed to provision and dispose commands.
const (
	LaneEnv      = "LERP_LANE"
	TicketIDEnv  = "LERP_TICKET_ID"
	WorkspaceEnv = "LERP_WORKSPACE"
)

// Identity identifies the lane workspace a command is operating on.
type Identity struct {
	Lane      int
	TicketID  string
	Workspace string
}

// Provision invokes command from repoDir, forwarding its output to log. A
// non-zero exit is returned to the caller, which must not start a run.
//
// The command inherits lerp's environment, minus the operator's Linear API
// key: see internal/childenv.
func Provision(ctx context.Context, repoDir, command string, id Identity, log io.Writer) error {
	log = writerOrDiscard(log)
	if err := invoke(ctx, repoDir, command, id, log); err != nil {
		fmt.Fprintf(log, "provision workspace: %v\n", err)
		return fmt.Errorf("provision workspace: %w", err)
	}
	return nil
}

// Dispose invokes command from repoDir, forwarding its output to log. Cleanup
// failures are logged but deliberately do not prevent the caller from reaping
// the lane.
func Dispose(ctx context.Context, repoDir, command string, id Identity, log io.Writer) {
	log = writerOrDiscard(log)
	if err := invoke(ctx, repoDir, command, id, log); err != nil {
		fmt.Fprintf(log, "dispose workspace: %v\n", err)
	}
}

func writerOrDiscard(log io.Writer) io.Writer {
	if log == nil {
		return io.Discard
	}
	return log
}

func invoke(ctx context.Context, repoDir, command string, id Identity, log io.Writer) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = repoDir
	cmd.Env = childenv.Inherited(
		fmt.Sprintf("%s=%d", LaneEnv, id.Lane),
		TicketIDEnv+"="+id.TicketID,
		WorkspaceEnv+"="+id.Workspace,
	)
	cmd.Stdout = log
	cmd.Stderr = log
	return cmd.Run()
}
