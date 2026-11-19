package cli

import (
	"os/exec"
	"runtime"
)

// OpenBrowser opens url in the default browser. Best effort: on a headless machine it does nothing, and the URL is
// printed anyway.
func OpenBrowser(url string) {
	name, args := browserCommand(url, runtime.GOOS)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return
	}
	// reap it in the background, so the browser's exit does not leave a zombie
	go func() { _ = cmd.Wait() }()
}

// browserCommand is the command that opens url on goos.
func browserCommand(url, goos string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "cmd", []string{"/c", "start", "", url}
	}
	return "xdg-open", []string{url}
}
