// Command issue-timeline serves the timeline of GitHub, GitLab, Redmine and Jira repos from this machine: the web app,
// the API and the background syncs. Run `issue-timeline --help` for the options.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/ci"
	"gitlab.com/system4us/issue-timeline/internal/cli"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/github"
	"gitlab.com/system4us/issue-timeline/internal/gitlab"
	"gitlab.com/system4us/issue-timeline/internal/httpapi"
	"gitlab.com/system4us/issue-timeline/internal/webui"
)

// version is the release, set at build time: go build -ldflags "-X main.version=1.0.0".
var version = "dev"

const defaultPort = 4310

// githubRepoKey matches a GitHub repo key (owner/name). Any other repo key is GitLab, Redmine or Jira.
var githubRepoKey = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the command: it returns the exit code.
func run(argv []string) int {
	dir := cli.DefaultStoreDir(os.Getenv, cwd(), runtime.GOOS, home())
	if len(argv) > 0 && argv[0] == "credentials" {
		st := &credentials.Store{Dir: dir, Env: os.Getenv, Keyring: credentials.SystemKeyring()}
		return cli.CredentialsCommand(argv[1:], st, os.Stdin, os.Stdout, os.Stderr)
	}
	if len(argv) == 1 && (argv[0] == "--version" || argv[0] == "-v") {
		fmt.Println("issue-timeline", version)
		return 0
	}
	args := cli.ParseArgs(argv)
	if args.Help {
		fmt.Println(cli.Help)
		return 0
	}
	if args.Stop {
		pid, err := cli.StopBackground(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if pid == 0 {
			fmt.Println("issue-timeline is not running in the background.")
		} else {
			fmt.Printf("issue-timeline stopped (pid %d).\n", pid)
		}
		return 0
	}

	port, err := portFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	host := os.Getenv("HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	shown := browseURL(host, port)

	if args.Background && os.Getenv("ISSUE_TIMELINE_CHILD") == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		pid, err := cli.StartBackground(dir, argv, exe)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("issue-timeline is running in the background (pid %d).\n  Open:  %s\n  Logs:  %s/issue-timeline.log\n  Stop:  issue-timeline stop\n", pid, shown, dir)
		if cli.ShouldOpen(args.NoOpen, os.Getenv, term.IsTerminal(int(os.Stdout.Fd()))) {
			cli.OpenBrowser(shown)
		}
		return 0
	}

	return serve(args, host, port, dir)
}

// serve runs the server until SIGINT or SIGTERM.
func serve(args cli.Args, host string, port int, dir string) int {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			fmt.Fprintf(os.Stderr, "Port %d is already in use. Is issue-timeline already running? Set PORT to use another.\n", port)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	defer ln.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf := func(format string, a ...any) { log.Printf(format, a...) }
	poll := 5 * time.Minute
	if v := os.Getenv("POLL_MINUTES"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			poll = time.Duration(n * float64(time.Minute))
		}
	}
	a, err := app.New(app.Config{
		StoreDir:     dir,
		PollInterval: poll,
		Env:          os.Getenv,
		Secrets:      &credentials.Store{Dir: dir, Env: os.Getenv, Keyring: credentials.SystemKeyring()},
		Log:          logf,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	handler := httpapi.New(a, httpapi.Options{
		BindHost: host,
		Port:     port,
		Env:      os.Getenv,
		Sessions: auth.NewSessions(auth.SessionOptions{Env: os.Getenv}),
		UI:       webui.FS(),
		Log:      logf,
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	repos := a.Repos()
	printBanner(browseURL(host, port), repos, dir)
	if cli.ShouldOpen(args.NoOpen, os.Getenv, term.IsTerminal(int(os.Stdout.Fd()))) {
		cli.OpenBrowser(browseURL(host, port))
	}
	go a.Start(ctx)
	go logIdentities(ctx, repos)

	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case <-ctx.Done():
	}
	// the event streams end first: Shutdown waits for open requests, and a stream never ends on its own
	a.Stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// printBanner is the startup message: where to open the page, the repos, the data folder and how each host is signed in.
func printBanner(url string, repos []string, dir string) {
	reposText := strings.Join(repos, ", ")
	if reposText == "" {
		reposText = "none yet, add one from the page"
	}
	fmt.Printf("\nissue-timeline is running\n  Open:   %s\n  Repos:  %s\n  Data:   %s\n  Auth:   your gh and glab sessions and the stored credentials (edits only on repos in write mode)\n", url, reposText, dir)
	for _, line := range cli.AuthSummary(os.Getenv, cli.CheckCommand) {
		fmt.Printf("          %s\n", line)
	}
	fmt.Print("  Stop:   Ctrl+C\n\n")
}

// logIdentities logs whose account each token is, read from the host at startup. A token that is missing or refused is
// logged as such and does not stop the server.
func logIdentities(ctx context.Context, repos []string) {
	hasGitHub := false
	var gitlabHosts []string
	for _, repo := range repos {
		switch {
		case strings.HasPrefix(repo, "gitlab:"):
			u, err := url.Parse(strings.TrimPrefix(repo, "gitlab:"))
			if err == nil && !slices.Contains(gitlabHosts, u.Host) {
				gitlabHosts = append(gitlabHosts, u.Host)
			}
		case githubRepoKey.MatchString(repo):
			hasGitHub = true
		}
	}
	if hasGitHub {
		source := "gh session"
		if os.Getenv("GITHUB_TOKEN") != "" {
			source = "GITHUB_TOKEN"
		}
		login, err := github.Identity(ctx, github.Options{Token: github.TokenSource(), Categorize: ci.Categorize})
		if err != nil {
			log.Printf("GitHub: could not read the account of the %s token: %v", source, err)
		} else {
			log.Printf("GitHub: signed in as %s (token from %s)", login, source)
		}
	}
	for _, host := range gitlabHosts {
		login, source, err := gitlab.Identity(ctx, host, gitlab.Options{Token: gitlab.TokenSource(), Categorize: ci.Categorize})
		if err != nil {
			log.Printf("GitLab %s: could not read the account: %v", host, err)
		} else {
			log.Printf("GitLab %s: signed in as %s (token from %s)", host, login, source)
		}
	}
}

// browseURL is the address the page is opened at: the bind address, or localhost when bound to every interface.
func browseURL(host string, port int) string {
	if host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(port)))
}

// portFromEnv is PORT, or the default port when it is not set.
func portFromEnv() (int, error) {
	v := os.Getenv("PORT")
	if v == "" {
		return defaultPort, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("PORT must be a port number, not %q", v)
	}
	return n, nil
}

func cwd() string {
	d, _ := os.Getwd()
	return d
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}
