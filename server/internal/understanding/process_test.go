//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package understanding

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const descendantExitTimeout = 3 * time.Second
const descendantPollInterval = 10 * time.Millisecond

func TestRunStopsDescendantsAfterParentExit(t *testing.T) {
	shell, err := exec.LookPath("sh")

	if err != nil {
		t.Skip("sh is unavailable")
	}
	for _, test := range []struct {
		name     string
		redirect string
	}{
		{name: "inherited output pipes", redirect: ""},
		{name: "closed output pipes", redirect: ">/dev/null 2>&1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "child.pid")
			script := "sleep 60 " + test.redirect + " &\necho $! > \"$1\"\nexit 0\n"
			Run(context.Background(), []string{shell, "-c", script, "plugin", pidPath}, nil, nil)
			pidBytes, err := os.ReadFile(pidPath)

			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))

			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			deadline := time.Now().Add(descendantExitTimeout)

			for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				if time.Now().After(deadline) {
					t.Fatal("plugin descendant outlived completed attempt")
				}
				time.Sleep(descendantPollInterval)
			}
		})
	}
}
