//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package understanding

import "os/exec"

func configureProcess(command *exec.Cmd) {}
