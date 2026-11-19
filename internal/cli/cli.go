// Package cli is the command line behaviour: the flags, where the data lives, opening the browser, running in the
// background, and the `credentials` subcommand (credentials.go).
package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Help is the text of `issue-timeline --help`.
const Help = `issue-timeline: a timeline of the issues and pull/merge requests of GitHub, GitLab, Redmine and Jira repositories

Usage: issue-timeline [options]
       issue-timeline stop
       issue-timeline credentials set|list|clear [name] [host]

  -b, --background   keep running after the terminal closes (logs in the data folder)
      --no-open      do not open the browser
  -h, --help         show this help

Sign-in: it uses your existing sessions, nothing to configure. GitHub: gh auth login (or GITHUB_TOKEN).
GitLab: glab auth login (or GITLAB_TOKEN). Redmine and Jira: save the key or token with
"issue-timeline credentials set" (or REDMINE_API_KEY, JIRA_EMAIL, JIRA_API_TOKEN). Secrets saved that way stay in the
OS keyring, or in an owner-only file in the data folder, and go only to the site they belong to.

Environment: PORT, HOST, STORE_DIR, POLL_MINUTES, GITHUB_TOKEN, GITHUB_HOST, GITLAB_TOKEN, VOCABULARY_FILE,
ALLOWED_HOSTS (more host names this server answers to, comma separated), COOKIE_SECURE=1 (Secure session cookie,
for HTTPS behind a proxy), SESSION_IDLE_HOURS (12: a session ends after that long without a request),
SESSION_MAX_DAYS (7: and at most that long after it began), WEBHOOK_SECRET (turns on POST /api/webhook, the one
webhook every host posts to), GITHUB_OAUTH_CLIENT_ID and _SECRET, GITLAB_OAUTH_CLIENT_ID and _SECRET (sign-in on the
host's own page), PUBLIC_URL (the address the hosts send people back to). See the README.`

// Args are the flags the command takes.
type Args struct {
	Help       bool
	Background bool
	NoOpen     bool
	Stop       bool
}

// ParseArgs reads the flags. "stop" must be the first argument.
func ParseArgs(argv []string) Args {
	has := func(names ...string) bool {
		for _, a := range argv {
			for _, n := range names {
				if a == n {
					return true
				}
			}
		}
		return false
	}
	return Args{
		Help:       has("-h", "--help"),
		Background: has("-b", "--background"),
		NoOpen:     has("--no-open"),
		Stop:       len(argv) > 0 && argv[0] == "stop",
	}
}

// DefaultStoreDir is where the data is kept: STORE_DIR; the ./data of cwd when it already holds a repository list (the
// layout before this default existed); otherwise a folder of the user, so running the command in a project does not drop
// a data folder inside it. goos is the operating system ("linux", "darwin", "windows"); home is the user's folder.
func DefaultStoreDir(env func(string) string, cwd, goos, home string) string {
	if env == nil {
		env = os.Getenv
	}
	if d := env("STORE_DIR"); d != "" {
		return d
	}
	if _, err := os.Stat(filepath.Join(cwd, "data", "repos.json")); err == nil {
		return filepath.Join(cwd, "data")
	}
	switch goos {
	case "windows":
		appData := env("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "issue-timeline")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "issue-timeline")
	}
	xdg := env("XDG_DATA_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdg, "issue-timeline")
}

// ShouldOpen is whether the browser opens by itself: not when asked not to, not on a non-interactive terminal, not in CI.
func ShouldOpen(noOpen bool, env func(string) string, isTTY bool) bool {
	if env == nil {
		env = os.Getenv
	}
	return !noOpen && isTTY && env("CI") == "" && env("NO_OPEN") == ""
}

// AuthSummary says how each host is signed in, for the startup message. It never shows a token. run runs a command and
// returns an error when it fails (nil on exit status 0); nil means CheckCommand.
func AuthSummary(env func(string) string, run func(name string, args ...string) error) []string {
	if env == nil {
		env = os.Getenv
	}
	if run == nil {
		run = CheckCommand
	}
	github := signedIn(env, run, "GITHUB_TOKEN", "gh", "gh session")
	gitlab := signedIn(env, run, "GITLAB_TOKEN", "glab", "glab session")
	return []string{
		"GitHub: " + orHint(github, "not signed in. Run `gh auth login` or set GITHUB_TOKEN"),
		"GitLab: " + orHint(gitlab, "not signed in. Run `glab auth login` or set GITLAB_TOKEN"),
	}
}

// signedIn is the name of where a host's sign-in is: its token variable, or the CLI's session; "" when neither.
func signedIn(env func(string) string, run func(string, ...string) error, tokenVar, cli, session string) string {
	if env(tokenVar) != "" {
		return tokenVar
	}
	if run(cli, "auth", "status") == nil {
		return session
	}
	return ""
}

func orHint(v, hint string) string {
	if v == "" {
		return hint
	}
	return v
}

// CheckCommand runs a command quietly and returns its error. The first read of a credential from the OS keyring can take
// several seconds, so it has a 20 s limit: a slow answer reads as signed out otherwise.
func CheckCommand(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}
