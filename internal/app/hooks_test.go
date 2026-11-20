package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// regHost is a GitHub-like repo that can have a webhook registered: it records what it was asked.
type regHost struct {
	base
	mu   *sync.Mutex
	log  *[]string
	fail error
}

func (h *regHost) EnsureWebhook(_ context.Context, spec provider.HookSpec) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.log = append(*h.log, "ensure "+h.key+" "+spec.URL+" "+spec.Secret)
	if h.fail != nil {
		return "", h.fail
	}
	return "created", nil
}

func (h *regHost) RemoveWebhook(_ context.Context, url string) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.log = append(*h.log, "remove "+h.key+" "+url)
	return true, nil
}

type hookRig struct {
	a     *App
	mu    sync.Mutex
	calls []string
	logs  []string
}

func (r *hookRig) snapshot() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...), append([]string(nil), r.logs...)
}

// waitCalls waits until the host got n calls (registration runs in the background).
func (r *hookRig) waitCalls(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls, _ := r.snapshot()
		if len(calls) >= n || time.Now().After(deadline) {
			if len(calls) < n {
				t.Fatalf("want %d webhook calls, got %v", n, calls)
			}
			return calls
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newHookRig(t *testing.T, env map[string]string, fail error, repos ...string) *hookRig {
	t.Helper()
	r := &hookRig{}
	dir := t.TempDir()
	if len(repos) > 0 {
		writeRepos(t, dir, repos...)
	}
	a, err := New(Config{
		StoreDir:     dir,
		PollInterval: time.Hour,
		Env:          func(k string) string { return env[k] },
		Log: func(format string, args ...any) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.logs = append(r.logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
		},
		Hosts: func(repo string) (provider.Host, error) {
			return &regHost{base: base{key: repo, prov: "github", origin: "github"}, mu: &r.mu, log: &r.calls, fail: fail}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.Stop()
		for _, repo := range a.Repos() {
			waitIdle(t, a, repo)
		}
	})
	r.a = a
	return r
}

var on = map[string]string{"REGISTER_WEBHOOK": "true", "WEBHOOK_SECRET": "s3cret", "PUBLIC_URL": "https://timeline.example.com/"}

func TestHookTarget(t *testing.T) {
	with := func(over map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range on {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name   string
		env    map[string]string
		url    string
		reason string
	}{
		{"off by default", map[string]string{"WEBHOOK_SECRET": "s", "PUBLIC_URL": "https://t.example.com"}, "", ""},
		{"off when false", with(map[string]string{"REGISTER_WEBHOOK": "false"}), "", ""},
		{"on", with(nil), "https://timeline.example.com/api/webhook", ""},
		{"1 and yes count as on", with(map[string]string{"REGISTER_WEBHOOK": "1"}), "https://timeline.example.com/api/webhook", ""},
		{"needs the secret", with(map[string]string{"WEBHOOK_SECRET": ""}), "", "WEBHOOK_SECRET"},
		{"needs a public url", with(map[string]string{"PUBLIC_URL": ""}), "", "PUBLIC_URL is empty"},
		{"localhost", with(map[string]string{"PUBLIC_URL": "http://localhost:4310"}), "", "localhost"},
		{"loopback", with(map[string]string{"PUBLIC_URL": "http://127.0.0.1:4310"}), "", "private address"},
		{"private range", with(map[string]string{"PUBLIC_URL": "http://192.168.1.5:4310"}), "", "private address"},
		{"tailscale", with(map[string]string{"PUBLIC_URL": "http://100.83.173.113:4310"}), "", "private address"},
		{"a public ip", with(map[string]string{"PUBLIC_URL": "https://203.0.113.9"}), "https://203.0.113.9/api/webhook", ""},
		{"not http", with(map[string]string{"PUBLIC_URL": "ftp://timeline.example.com"}), "", "http(s)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newHookRig(t, c.env, nil)
			spec, reason := r.a.impl.hookTarget()
			if spec.URL != c.url {
				t.Fatalf("url %q, want %q (reason %q)", spec.URL, c.url, reason)
			}
			if c.reason == "" && reason != "" || c.reason != "" && !strings.Contains(reason, c.reason) {
				t.Fatalf("reason %q, want it to mention %q", reason, c.reason)
			}
			if c.url != "" && spec.Secret != c.env["WEBHOOK_SECRET"] {
				t.Fatalf("secret %q", spec.Secret)
			}
		})
	}
}

func TestAddingARepoRegistersItsWebhook(t *testing.T) {
	r := newHookRig(t, on, nil)
	repo, added, err := r.a.AddRepo(context.Background(), "github", "acme/app", "read")
	if err != nil || !added {
		t.Fatalf("%q %v %v", repo, added, err)
	}
	calls := r.waitCalls(t, 1)
	if calls[0] != "ensure acme/app https://timeline.example.com/api/webhook s3cret" {
		t.Fatalf("calls %v", calls)
	}
}

func TestStartupRegistersEveryRepo(t *testing.T) {
	r := newHookRig(t, on, nil, "acme/app", "acme/web")
	r.a.impl.registerHooks()
	calls := r.waitCalls(t, 2)
	if !(strings.Contains(calls[0], "acme/app") && strings.Contains(calls[1], "acme/web")) {
		t.Fatalf("calls %v", calls)
	}
}

func TestRemovingARepoRemovesItsWebhook(t *testing.T) {
	r := newHookRig(t, on, nil, "acme/app")
	if _, err := r.a.RemoveRepo("acme/app"); err != nil {
		t.Fatal(err)
	}
	calls := r.waitCalls(t, 1)
	if calls[0] != "remove acme/app https://timeline.example.com/api/webhook" {
		t.Fatalf("calls %v", calls)
	}
}

func TestNothingIsRegisteredWhenItIsOff(t *testing.T) {
	r := newHookRig(t, map[string]string{"WEBHOOK_SECRET": "s", "PUBLIC_URL": "https://timeline.example.com"}, nil, "acme/app")
	r.a.impl.registerHooks()
	if _, err := r.a.RemoveRepo("acme/app"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if calls, _ := r.snapshot(); len(calls) != 0 {
		t.Fatalf("calls %v", calls)
	}
}

func TestARefusalIsLoggedAndTheRepoKeepsWorking(t *testing.T) {
	r := newHookRig(t, on, provider.Errorf(403, "Must have admin rights to Repository."), "acme/app")
	r.a.impl.registerHooks()
	_, logs := r.snapshot()
	var found bool
	for _, l := range logs {
		if strings.Contains(l, "webhook not registered") && strings.Contains(l, "admin rights") {
			found = true
		}
	}
	if !found {
		t.Fatalf("logs %v", logs)
	}
	if got := r.a.Repos(); len(got) != 1 {
		t.Fatalf("repos %v", got)
	}
}
