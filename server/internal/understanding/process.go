package understanding

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Execution captures the process outcome. Stdout and Stderr contain the exact
// byte streams emitted by the child, including output from unsuccessful runs.
type Execution struct {
	StartedAt    time.Time
	CompletedAt  time.Time
	Duration     time.Duration
	ExitCode     *int
	ProcessError string
	Stdout       []byte
	Stderr       []byte
}

const processWaitDelay = time.Second

// Run executes one plugin command with requestBytes on stdin and captures both
// output streams concurrently through os/exec. A process failure is recorded
// in Execution so callers can persist the available exchange before reporting it.
func Run(
	ctx context.Context,
	argv []string,
	requestBytes []byte,
	liveStderr io.Writer,
) Execution {
	startedAt := time.Now().UTC()
	execution := Execution{
		StartedAt:    startedAt,
		CompletedAt:  time.Time{},
		Duration:     0,
		ExitCode:     nil,
		ProcessError: "",
		Stdout:       nil,
		Stderr:       nil,
	}
	if len(argv) == 0 {
		execution.ProcessError = "plugin command must contain an executable"
		execution.CompletedAt = time.Now().UTC()
		execution.Duration = execution.CompletedAt.Sub(startedAt)
		return execution
	}
	if !filepath.IsAbs(argv[0]) {
		execution.ProcessError = "plugin executable path must be absolute"
		execution.CompletedAt = time.Now().UTC()
		execution.Duration = execution.CompletedAt.Sub(startedAt)
		return execution
	}

	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	configureProcess(command)
	command.WaitDelay = processWaitDelay
	command.Stdin = bytes.NewReader(requestBytes)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = stderrCapture{capture: &stderr, forward: liveStderr}
	err := command.Run()
	// A parent can exit while descendants still hold output pipes or keep
	// running with redirected output. Release the worker slot only after
	// terminating the remaining process group.
	if command.Process != nil {
		if cleanupErr := command.Cancel(); cleanupErr != nil &&
			!errors.Is(cleanupErr, os.ErrProcessDone) {
			err = errors.Join(err, fmt.Errorf("stop plugin descendants: %w", cleanupErr))
		}
	}
	execution.CompletedAt = time.Now().UTC()
	execution.Duration = execution.CompletedAt.Sub(startedAt)
	execution.Stdout = stdout.Bytes()
	execution.Stderr = stderr.Bytes()
	if err == nil {
		code := 0
		execution.ExitCode = &code
		return execution
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		code := exitError.ExitCode()
		execution.ExitCode = &code
		execution.ProcessError = fmt.Sprintf("plugin exited with status %d", code)
		return execution
	}
	execution.ProcessError = fmt.Sprintf("launch plugin: %v", err)
	return execution
}

// stderrCapture always accepts the child bytes after capturing them. A failure
// in the live destination must not stop os/exec from draining the child's pipe.
type stderrCapture struct {
	capture *bytes.Buffer
	forward io.Writer
}

func (writer stderrCapture) Write(data []byte) (int, error) {
	_, _ = writer.capture.Write(data)
	if writer.forward != nil {
		_, _ = writer.forward.Write(data)
	}
	return len(data), nil
}
