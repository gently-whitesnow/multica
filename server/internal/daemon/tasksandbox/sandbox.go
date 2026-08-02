package tasksandbox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

const (
	RootHelperArg = "__multica_task_sandbox_root"
	EnvEnabled    = "MULTICA_TASK_SANDBOX_WRAPPER"
	EnvExecutable = "MULTICA_TASK_SANDBOX_EXECUTABLE"
	EnvTaskID     = "MULTICA_TASK_SANDBOX_TASK_ID"
	EnvRoot       = "MULTICA_TASK_SANDBOX_ROOT"
	EnvWorkDir    = "MULTICA_TASK_SANDBOX_WORKDIR"
	EnvSelf       = "MULTICA_TASK_SANDBOX_SELF"
	EnvDaemonPID  = "MULTICA_TASK_SANDBOX_DAEMON_PID"
)

// Session is a prepared, task-scoped OS identity. Close must run on every
// terminal path so the identity and every process carrying it are reclaimed.
type Session interface {
	Environment(realExecutable string) map[string]string
	Close(context.Context) error
}

type Stdio struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

func Prepare(ctx context.Context, self, taskID, root, workDir string, logger *slog.Logger) (Session, error) {
	return prepare(ctx, self, taskID, root, workDir, logger)
}

func RunWrapper(ctx context.Context, args []string, stdio Stdio) error {
	return runWrapper(ctx, args, stdio)
}

func RunRootHelper(args []string, stdio Stdio) error {
	if err := runRootHelper(args, stdio); err != nil {
		return fmt.Errorf("task sandbox helper: %w", err)
	}
	return nil
}
