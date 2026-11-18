package gitlab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The server's token for a host comes from the environment (GITLAB_TOKEN_<HOST>, then GITLAB_TOKEN for gitlab.com), or
// else from the glab session. An environment token needs no glab, so it works in containers, CI and on machines without
// glab. A token is kept for 20 minutes; a 401 from the host drops it.
const tokenTTL = 20 * time.Minute

// runFunc runs a command and returns its output. It is a field of the token source so a test can replace glab.
type runFunc func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

var (
	quickTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{20,}$`)
	tokenFoundPattern = regexp.MustCompile(`(?i)Token found`)
	authTokenPattern  = regexp.MustCompile(`(?i)Token found[^:\n]*:\s*(\S+)`)
)

// tokenEnvName is the environment variable that holds the host's token, or "" when none does.
func tokenEnvName(host string) string {
	specific := "GITLAB_TOKEN_" + strings.ToUpper(sanitizeEnvHost(host))
	if os.Getenv(specific) != "" {
		return specific
	}
	if host == "gitlab.com" && os.Getenv("GITLAB_TOKEN") != "" {
		return "GITLAB_TOKEN"
	}
	return ""
}

// sanitizeEnvHost turns every character that is not a letter or a digit into "_", as the environment names do.
func sanitizeEnvHost(host string) string {
	var b strings.Builder
	for _, c := range host {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// envToken is the token the environment gives for a host, or "".
func envToken(host string) string {
	if name := tokenEnvName(host); name != "" {
		return os.Getenv(name)
	}
	return ""
}

// tokenCache keeps the server's tokens per host. The first caller of a host runs the load in its own goroutine (the
// glab commands run there, with that caller's context); a caller that comes meanwhile waits for that one load and shares
// its answer. No lock is held while a load runs.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]tokenEntry
	flights map[string]*tokenFlight
}

type tokenEntry struct {
	token string
	until time.Time
}

type tokenFlight struct {
	done  chan struct{}
	token string
	err   error
}

func newTokenCache() *tokenCache {
	return &tokenCache{entries: map[string]tokenEntry{}, flights: map[string]*tokenFlight{}}
}

// defaultTokens is the cache of the server's tokens: one per process, as the Node server kept its auth cache.
var defaultTokens = newTokenCache()

// get returns the host's token: from the cache, from a load in flight, or by making the load.
func (c *tokenCache) get(ctx context.Context, host string, load func(context.Context) (string, error)) (string, error) {
	for {
		c.mu.Lock()
		if e, ok := c.entries[host]; ok && time.Now().Before(e.until) {
			c.mu.Unlock()
			return e.token, nil
		}
		if f, ok := c.flights[host]; ok {
			c.mu.Unlock()
			select {
			case <-f.done:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			// the caller that made the load left before it finished: this one makes it again
			if f.err != nil && isContextErr(f.err) && ctx.Err() == nil {
				continue
			}
			return f.token, f.err
		}
		f := &tokenFlight{done: make(chan struct{})}
		c.flights[host] = f
		c.mu.Unlock()

		lctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		tok, err := load(lctx)
		cancel()

		c.mu.Lock()
		if err == nil {
			c.entries[host] = tokenEntry{token: tok, until: time.Now().Add(tokenTTL)}
		}
		delete(c.flights, host)
		f.token, f.err = tok, err
		c.mu.Unlock()
		close(f.done)
		return tok, err
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// forget drops the host's token, after the host refused it.
func (c *tokenCache) forget(host string) {
	c.mu.Lock()
	delete(c.entries, host)
	c.mu.Unlock()
}

// forgetToken drops the server's token of a host from the default cache (a 401 from the host).
func forgetToken(host string) { defaultTokens.forget(host) }

// TokenSource is the server's token for a host: the environment, else glab. Each call of the returned function is cheap
// while the token is cached; a load of glab runs in its own goroutine and is shared by every caller for the same host.
func TokenSource() func(ctx context.Context, host string) (string, error) {
	return tokenSourceWith(defaultTokens, execRun)
}

func tokenSourceWith(c *tokenCache, run runFunc) func(ctx context.Context, host string) (string, error) {
	return func(ctx context.Context, host string) (string, error) {
		return c.get(ctx, host, func(ctx context.Context) (string, error) {
			return loadToken(ctx, host, run)
		})
	}
}

// loadToken reads the token from the environment, else from glab: `glab config get token` first (it reads glab's own
// store, without a request), then `glab auth status --show-token`, retried once, since a keyring read can be slow.
func loadToken(ctx context.Context, host string, run runFunc) (string, error) {
	if t := envToken(host); t != "" {
		return t, nil
	}
	if strings.HasPrefix(host, "-") {
		return "", errors.New("the host name starts with a dash")
	}
	quickCtx, cancelQuick := context.WithTimeout(ctx, 5*time.Second)
	out, _, err := run(quickCtx, "glab", "config", "get", "token", "--host", host)
	cancelQuick()
	if quick := strings.TrimSpace(out); err == nil && quickTokenPattern.MatchString(quick) {
		return quick, nil
	}
	var output string
	for attempt := 1; attempt <= 2; attempt++ {
		statusCtx, cancelStatus := context.WithTimeout(ctx, 20*time.Second)
		so, se, _ := run(statusCtx, "glab", "auth", "status", "--hostname", host, "--show-token")
		cancelStatus()
		output = so + "\n" + se
		if tokenFoundPattern.MatchString(output) {
			break
		}
		if attempt == 1 {
			if err := sleepCtx(ctx, 2*time.Second); err != nil {
				return "", err
			}
		}
	}
	m := authTokenPattern.FindStringSubmatch(output)
	if m == nil || m[1] == "" || strings.Trim(m[1], "*") == "" {
		return "", fmt.Errorf("could not read the glab session for %s. Run glab auth login --hostname %s", host, host)
	}
	// glab can print the credential even when its optional API check times out, so the token counts when it is there.
	return m[1], nil
}

// execRun runs a program (never through a shell) with the context's deadline.
func execRun(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se cappedBuffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	return so.String(), se.String(), err
}

// cappedBuffer keeps the first 1 MiB of a command's output.
type cappedBuffer struct{ buf bytes.Buffer }

const maxCommandOutput = 1 << 20

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := maxCommandOutput - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
