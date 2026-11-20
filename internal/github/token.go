package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// tokenTTL is how long a token read from the gh CLI is kept before gh is asked again.
const tokenTTL = 20 * time.Minute

// TokenSource is the server's token: GITHUB_TOKEN, or else `gh auth token --hostname <host>`. The gh run is cached for
// tokenTTL, and concurrent callers share one run. It only runs when a caller asks: nothing starts at init.
func TokenSource() func(ctx context.Context) (string, error) {
	return newTokenSource(EndpointsFromEnv().Host, os.Getenv, ghAuthToken)
}

// ghAuthToken runs the gh CLI for the token of host.
func ghAuthToken(ctx context.Context, host string) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host).Output()
	tok := strings.TrimSpace(string(out))
	if err != nil || tok == "" {
		return "", errors.New("No token: set GITHUB_TOKEN or log in with `gh auth login`.")
	}
	return tok, nil
}

// flight is one gh run that others may be waiting for.
type flight struct {
	done chan struct{}
	tok  string
	err  error
}

// tokenSource is TokenSource with its parts passed in, so tests can count the runs.
type tokenSource struct {
	host   string
	getenv func(string) string
	run    func(ctx context.Context, host string) (string, error)
	now    func() time.Time

	mu       sync.Mutex
	tok      string
	have     bool
	expires  time.Time
	inflight *flight
}

func newTokenSource(host string, getenv func(string) string, run func(context.Context, string) (string, error)) func(context.Context) (string, error) {
	ts := &tokenSource{host: host, getenv: getenv, run: run, now: time.Now}
	return ts.get
}

func (ts *tokenSource) get(ctx context.Context) (string, error) {
	if t := ts.getenv("GITHUB_TOKEN"); t != "" {
		return t, nil
	}
	ts.mu.Lock()
	if ts.have && ts.now().Before(ts.expires) {
		tok := ts.tok
		ts.mu.Unlock()
		return tok, nil
	}
	if f := ts.inflight; f != nil {
		// another caller is running gh: wait for its answer, or give up when this caller's context ends
		ts.mu.Unlock()
		select {
		case <-f.done:
			return f.tok, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	ts.inflight = f
	ts.mu.Unlock()

	// the run happens in this caller's goroutine: the server never blocks on it at start
	tok, err := ts.run(ctx, ts.host)

	ts.mu.Lock()
	if err == nil {
		ts.tok, ts.have, ts.expires = tok, true, ts.now().Add(tokenTTL)
	}
	ts.inflight = nil
	f.tok, f.err = tok, err
	close(f.done)
	ts.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("reading the gh token for %s: %w", ts.host, err)
	}
	return tok, nil
}
