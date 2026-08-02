//go:build !linux

package tasksandbox

import (
	"context"
	"errors"
	"log/slog"
)

var errUnsupported = errors.New("task sandbox is supported only on Linux")

func prepare(context.Context, string, string, string, string, *slog.Logger) (Session, error) {
	return nil, errUnsupported
}

func runWrapper(context.Context, []string, Stdio) error { return errUnsupported }
func runRootHelper([]string, Stdio) error               { return errUnsupported }
