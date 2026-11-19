package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDataFolderIsSTORE_DIRThenAPreviousDataFolderThenAUserFolder(t *testing.T) {
	cwd := t.TempDir()
	home := "/home/u"
	noEnv := func(string) string { return "" }
	if got := DefaultStoreDir(func(k string) string {
		if k == "STORE_DIR" {
			return "/x"
		}
		return ""
	}, cwd, "linux", home); got != "/x" {
		t.Fatalf("STORE_DIR: %s", got)
	}
	if got := DefaultStoreDir(noEnv, cwd, "linux", home); got != filepath.Join(home, ".local", "share", "issue-timeline") {
		t.Fatalf("linux: %s", got)
	}
	xdg := func(k string) string {
		if k == "XDG_DATA_HOME" {
			return "/xdg"
		}
		return ""
	}
	if got := DefaultStoreDir(xdg, cwd, "linux", home); got != filepath.Join("/xdg", "issue-timeline") {
		t.Fatalf("XDG: %s", got)
	}
	if got := DefaultStoreDir(noEnv, cwd, "darwin", home); got != filepath.Join(home, "Library", "Application Support", "issue-timeline") {
		t.Fatalf("darwin: %s", got)
	}
	appData := func(k string) string {
		if k == "APPDATA" {
			return `C:\Roaming`
		}
		return ""
	}
	if got := DefaultStoreDir(appData, cwd, "windows", home); got != filepath.Join(`C:\Roaming`, "issue-timeline") {
		t.Fatalf("windows: %s", got)
	}
	// a ./data folder with a repository list is the one used, as before the default existed
	if err := os.MkdirAll(filepath.Join(cwd, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "data", "repos.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DefaultStoreDir(noEnv, cwd, "linux", home); got != filepath.Join(cwd, "data") {
		t.Fatalf("./data: %s", got)
	}
}

func TestArgumentsAndWhenTheBrowserOpens(t *testing.T) {
	if a := ParseArgs([]string{"-b", "--no-open"}); a != (Args{Help: false, Background: true, NoOpen: true, Stop: false}) {
		t.Fatalf("args = %+v", a)
	}
	if !ParseArgs([]string{"stop"}).Stop {
		t.Fatal("stop not read")
	}
	if !ParseArgs([]string{"--help"}).Help || !ParseArgs([]string{"-h"}).Help {
		t.Fatal("help not read")
	}
	if ParseArgs(nil).Stop {
		t.Fatal("no arguments is a stop")
	}
	none := func(string) string { return "" }
	if !ShouldOpen(false, none, true) {
		t.Fatal("interactive terminal does not open")
	}
	if ShouldOpen(true, none, true) {
		t.Fatal("--no-open opens")
	}
	if ShouldOpen(false, func(k string) string {
		if k == "CI" {
			return "1"
		}
		return ""
	}, true) {
		t.Fatal("CI opens")
	}
	if ShouldOpen(false, none, false) {
		t.Fatal("a pipe opens")
	}
	if ShouldOpen(false, func(k string) string {
		if k == "NO_OPEN" {
			return "1"
		}
		return ""
	}, true) {
		t.Fatal("NO_OPEN opens")
	}
}

func TestTheStartupAuthSummaryShowsEnvTokensCLISessionsOrAHint(t *testing.T) {
	ok := func(string, ...string) error { return nil }
	no := func(string, ...string) error { return errors.New("exit status 1") }
	none := func(string) string { return "" }
	got := AuthSummary(none, ok)
	if got[0] != "GitHub: gh session" || got[1] != "GitLab: glab session" {
		t.Fatalf("sessions: %v", got)
	}
	tokens := func(k string) string {
		switch k {
		case "GITHUB_TOKEN", "GITLAB_TOKEN":
			return "secret-value"
		}
		return ""
	}
	got = AuthSummary(tokens, no)
	if got[0] != "GitHub: GITHUB_TOKEN" || got[1] != "GitLab: GITLAB_TOKEN" {
		t.Fatalf("tokens: %v", got)
	}
	for _, line := range got {
		if strings.Contains(line, "secret-value") {
			t.Fatal("a token value is printed")
		}
	}
	if hint := AuthSummary(none, no)[1]; !strings.Contains(hint, "glab auth login") {
		t.Fatalf("hint: %s", hint)
	}
}

func TestTheBackgroundChildRunsWithoutTheBackgroundFlagAndWithoutABrowser(t *testing.T) {
	got := backgroundArgs([]string{"-b", "--port", "9", "--background", "--no-open"})
	if strings.Join(got, " ") != "--port 9 --no-open --no-open" {
		t.Fatalf("args = %v", got)
	}
}

func TestStopWithNoPidFileStopsNothingAndAGarbledOneIsAnError(t *testing.T) {
	dir := t.TempDir()
	if pid, err := StopBackground(dir); pid != 0 || err != nil {
		t.Fatalf("no pid file: %d, %v", pid, err)
	}
	path := filepath.Join(dir, pidFile)
	for _, bad := range []string{"", "abc", "0", "-1"} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := StopBackground(dir); err == nil {
			t.Fatalf("pid file %q: no error (pid 0 would signal the whole process group)", bad)
		}
	}
}

func TestTheHelpNamesTheCommandsAndEveryEnvironmentVariable(t *testing.T) {
	for _, want := range []string{"credentials set|list|clear", "ALLOWED_HOSTS", "COOKIE_SECURE=1", "SESSION_IDLE_HOURS", "SESSION_MAX_DAYS", "--background", "stop"} {
		if !strings.Contains(Help, want) {
			t.Errorf("help does not mention %s", want)
		}
	}
}

func TestBackgroundAndBrowserCommandsPerSystem(t *testing.T) {
	if name, args := browserCommand("http://x", "darwin"); name != "open" || args[0] != "http://x" {
		t.Fatalf("darwin: %s %v", name, args)
	}
	if name, args := browserCommand("http://x", "windows"); name != "cmd" || args[1] != "start" {
		t.Fatalf("windows: %s %v", name, args)
	}
	if name, _ := browserCommand("http://x", "linux"); name != "xdg-open" {
		t.Fatalf("linux: %s", name)
	}
}
