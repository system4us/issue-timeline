package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	logFile = "issue-timeline.log"
	pidFile = "issue-timeline.pid"
)

// backgroundArgs are the arguments the detached child runs with: the same ones, minus the background flag, and never
// opening a browser.
func backgroundArgs(argv []string) []string {
	out := make([]string, 0, len(argv)+1)
	for _, a := range argv {
		if a != "-b" && a != "--background" {
			out = append(out, a)
		}
	}
	return append(out, "--no-open")
}

// StartBackground runs exe (this same program) detached from the terminal, with the arguments of argv, STORE_DIR set to
// dir and ISSUE_TIMELINE_CHILD=1. Its output goes to <dir>/issue-timeline.log, and its pid to <dir>/issue-timeline.pid.
// It returns the pid.
func StartBackground(dir string, argv []string, exe string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("creating %s: %w", dir, err)
	}
	logPath := filepath.Join(dir, logFile)
	log, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", logPath, err)
	}
	defer log.Close()

	cmd := exec.Command(exe, backgroundArgs(argv)...)
	cmd.Stdout = log
	cmd.Stderr = log
	// the last value of a duplicate key wins in exec, so these override the parent's environment
	cmd.Env = append(os.Environ(), "STORE_DIR="+dir, "ISSUE_TIMELINE_CHILD=1")
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting %s: %w", exe, err)
	}
	pid := cmd.Process.Pid
	// the child runs on its own: nothing waits for it here
	_ = cmd.Process.Release()
	if err := os.WriteFile(filepath.Join(dir, pidFile), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return pid, fmt.Errorf("writing the pid file: %w", err)
	}
	return pid, nil
}

// StopBackground sends SIGTERM to the process in <dir>/issue-timeline.pid and removes the file. It returns the pid, or 0
// when there is no pid file (nothing is running from this folder). A pid file that does not hold a positive number is an
// error: the pid 0 would signal the whole process group.
func StopBackground(dir string) (int, error) {
	path := filepath.Join(dir, pidFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("%s does not hold a process id", path)
	}
	// a process that is gone already is the same as a stopped one
	_ = terminate(pid)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return pid, fmt.Errorf("removing %s: %w", path, err)
	}
	return pid, nil
}

// terminate asks a process to stop (SIGTERM); where a signal cannot be sent (Windows), it is killed.
func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return p.Kill()
	}
	return nil
}
