package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func TestEndpointsFromEnvDefaultsAndEnterprise(t *testing.T) {
	none := func(string) string { return "" }
	got := endpointsFrom(none)
	want := Endpoints{Host: "github.com", REST: "https://api.github.com", GraphQL: "https://api.github.com/graphql"}
	if got != want {
		t.Fatalf("default endpoints: got %+v, want %+v", got, want)
	}
	ghe := endpointsFrom(mapGetter{"GITHUB_HOST": "https://ghe.example.com/"}.get)
	if ghe.REST != "https://ghe.example.com/api/v3" || ghe.GraphQL != "https://ghe.example.com/api/graphql" || ghe.Host != "ghe.example.com" {
		t.Fatalf("enterprise endpoints: got %+v", ghe)
	}
	over := endpointsFrom(mapGetter{"GITHUB_HOST": "ghe.example.com", "GITHUB_API_URL": "https://api.example.com/", "GITHUB_GRAPHQL_URL": "https://gql.example.com/g"}.get)
	if over.REST != "https://api.example.com" || over.GraphQL != "https://gql.example.com/g" {
		t.Fatalf("overrides: got %+v", over)
	}
}

// get is a map as a getenv function.
func (m mapGetter) get(k string) string { return m[k] }

type mapGetter map[string]string

func TestGraphQLNotFoundKeepsTheRestOfTheData(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"repository":{"x":1}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve"}]}`))
	})
	r, _ := testRepo(t, h)
	var out struct {
		Repository struct {
			X int `json:"x"`
		} `json:"repository"`
	}
	if err := r.api.graphqlInto(context.Background(), r.o.Endpoints.GraphQL, "t", "query", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Repository.X != 1 {
		t.Fatalf("data lost: %+v", out)
	}
}

func TestGraphQLOtherErrorsFail(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"type":"FORBIDDEN","message":"no"}]}`))
	})
	r, _ := testRepo(t, h)
	_, err := r.api.graphql(context.Background(), r.o.Endpoints.GraphQL, "t", "query", nil)
	if err == nil || err.Error() != "GitHub GraphQL: no" {
		t.Fatalf("want the FORBIDDEN error, got %v", err)
	}
}

func TestGraphQLRetriesAnAnswerCutShort(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":{"ok":`)) // cut short
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	})
	r, _ := testRepo(t, h)
	var out struct {
		OK bool `json:"ok"`
	}
	if err := r.api.graphqlInto(context.Background(), r.o.Endpoints.GraphQL, "t", "query", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || calls != 2 {
		t.Fatalf("want the second answer after one retry: ok=%v calls=%d", out.OK, calls)
	}
}

func TestRateLimitIsRetriedWithRetryAfter(t *testing.T) {
	calls := 0
	var waited []time.Duration
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	})
	r, _ := testRepo(t, h)
	r.api.pause = func(_ context.Context, d time.Duration) error { waited = append(waited, d); return nil }
	if _, err := r.api.send(context.Background(), "GET", r.o.Endpoints.REST+"/x", "t", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(waited) != 1 || waited[0] != 2*time.Second {
		t.Fatalf("calls=%d waited=%v", calls, waited)
	}
}

func TestRetryWaitIsCappedAt15Seconds(t *testing.T) {
	if d := retryWait("600", time.Second); d != 15*time.Second {
		t.Fatalf("got %v", d)
	}
	if d := retryWait("", 3*time.Second); d != 3*time.Second {
		t.Fatalf("no header: got %v", d)
	}
}

func TestTokenSourceSharesOneGhRun(t *testing.T) {
	var runs atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	run := func(ctx context.Context, host string) (string, error) {
		if runs.Add(1) == 1 {
			started <- struct{}{}
		}
		<-release // the run is in flight until the test lets it finish
		return "gh-token", nil
	}
	tok := newTokenSource("github.com", func(string) string { return "" }, run)

	var wg sync.WaitGroup
	results := make([]string, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = tok(context.Background())
		}(i)
	}
	<-started
	close(release)
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != "gh-token" {
			t.Fatalf("caller %d: %q, %v", i, results[i], errs[i])
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("gh ran %d times, want 1", n)
	}
	// and the answer is kept: a later caller does not run gh again
	if _, err := tok(context.Background()); err != nil || runs.Load() != 1 {
		t.Fatalf("cached answer not used: runs=%d err=%v", runs.Load(), err)
	}
}

func TestTokenSourceEnvWinsAndCacheExpires(t *testing.T) {
	var runs atomic.Int32
	run := func(context.Context, string) (string, error) {
		runs.Add(1)
		return fmt.Sprintf("gh-%d", runs.Load()), nil
	}
	env := mapGetter{}
	ts := &tokenSource{host: "github.com", getenv: env.get, run: run, now: time.Now}
	get := ts.get

	env["GITHUB_TOKEN"] = "from-env"
	if tok, _ := get(context.Background()); tok != "from-env" || runs.Load() != 0 {
		t.Fatalf("GITHUB_TOKEN should win without running gh: %q runs=%d", tok, runs.Load())
	}
	delete(env, "GITHUB_TOKEN")
	base := time.Now()
	ts.now = func() time.Time { return base }
	if tok, _ := get(context.Background()); tok != "gh-1" {
		t.Fatalf("first gh token: %q", tok)
	}
	ts.now = func() time.Time { return base.Add(19 * time.Minute) }
	if tok, _ := get(context.Background()); tok != "gh-1" || runs.Load() != 1 {
		t.Fatalf("kept for 20 minutes: %q runs=%d", tok, runs.Load())
	}
	ts.now = func() time.Time { return base.Add(21 * time.Minute) }
	if tok, _ := get(context.Background()); tok != "gh-2" {
		t.Fatalf("read again after 20 minutes: %q", tok)
	}
}

func TestRoleFromPermissions(t *testing.T) {
	cases := []struct {
		p    *Permissions
		want provider.Role
	}{
		{&Permissions{Admin: true, Maintain: true, Push: true, Triage: true, Pull: true}, provider.RoleAdmin},
		{&Permissions{Push: true, Triage: true, Pull: true}, provider.RoleEditor},
		{&Permissions{Pull: true}, provider.RoleViewer},
		{&Permissions{Triage: true, Pull: true}, provider.RoleEditor},
		{&Permissions{Pull: false}, provider.RoleNone},
		{nil, provider.RoleNone},
	}
	for i, c := range cases {
		if got := RoleFromPermissions(c.p); got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
}

func TestRoleOfReadsThePersonsOwnPermissions(t *testing.T) {
	host := &fakeHost{routes: map[string]route{
		"GET /repos/o/r": {body: `{"permissions":{"pull":true,"push":false}}`},
	}}
	r, _ := testRepo(t, host)
	role, err := r.RoleOf(context.Background(), provider.Cred{Token: "person"}, provider.User{})
	if err != nil || role != provider.RoleViewer {
		t.Fatalf("got %q, %v", role, err)
	}
	if c := host.got()[0]; c.Path != "/repos/o/r" {
		t.Fatalf("asked %s", c.Path)
	}
}

func TestRoleOfRepoNotSeenIsNoRole(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		host := &fakeHost{routes: map[string]route{"GET /repos/o/r": {status: status, body: `{"message":"no"}`}}}
		r, _ := testRepo(t, host)
		role, err := r.RoleOf(context.Background(), provider.Cred{Token: "p"}, provider.User{})
		if err != nil || role != provider.RoleNone {
			t.Fatalf("status %d: got %q, %v; want no role and no error", status, role, err)
		}
	}
	host := &fakeHost{routes: map[string]route{"GET /repos/o/r": {status: 500, body: `{}`}}}
	r, _ := testRepo(t, host)
	r.api.pause = func(context.Context, time.Duration) error { return nil }
	if _, err := r.RoleOf(context.Background(), provider.Cred{Token: "p"}, provider.User{}); err == nil {
		t.Fatal("a server error is not no role")
	}
}

func TestVisibilityIsTheZeroValue(t *testing.T) {
	r, _ := testRepo(t, &fakeHost{})
	v, err := r.VisibilityOf(context.Background(), provider.Cred{}, provider.User{}, provider.RoleViewer, nil)
	if err != nil || v.Issues != nil || v.HiddenComments != nil || v.HideCode {
		t.Fatalf("got %+v, %v", v, err)
	}
}

func TestWhoAmIAndIdentity(t *testing.T) {
	host := &fakeHost{routes: map[string]route{
		"GET /user":     {body: `{"id":9,"login":"ana","name":null}`},
		"POST /graphql": {body: `{"data":{"viewer":{"login":"ana"}}}`},
	}}
	r, _ := testRepo(t, host)
	u, err := r.WhoAmI(context.Background(), "t")
	if err != nil || u.ID != 9 || u.Login != "ana" || u.Name != "ana" {
		t.Fatalf("whoami: %+v, %v", u, err)
	}
	login, err := Identity(context.Background(), r.o)
	if err != nil || login != "ana" {
		t.Fatalf("identity: %q, %v", login, err)
	}
}

func TestWhoAmIRefusedTokenIsA401(t *testing.T) {
	host := &fakeHost{routes: map[string]route{"GET /user": {status: 401, body: `{"message":"Bad credentials"}`}}}
	r, _ := testRepo(t, host)
	_, err := r.WhoAmI(context.Background(), "bad")
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("want a 401 *provider.Error, got %v", err)
	}
}

func TestExistsSaysNotFoundWhenTheRepoIsNull(t *testing.T) {
	host := &fakeHost{routes: map[string]route{"POST /graphql": {body: `{"data":{"repository":null}}`}}}
	r, _ := testRepo(t, host)
	if err := r.Exists(context.Background()); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestKeyProviderOrigin(t *testing.T) {
	r := New("owner", "name", Options{})
	if r.Key() != "owner/name" || r.Provider() != "github" || r.Origin() != "github" {
		t.Fatalf("got %s %s %s", r.Key(), r.Provider(), r.Origin())
	}
}
