package redmine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// A redirect to another site must not carry the key there: the second server never receives it, and the caller gets a
// 502 that names the cause.
func TestTheKeyIsNotSentToAnotherSiteOnARedirect(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Redmine-API-Key") != "" {
			leaked.Add(1)
		}
		respond(w, 200, map[string]any{})
	}))
	t.Cleanup(other.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(first.Close)

	r, err := New("redmine:"+first.URL+"/backend", Options{Key: keyOf("secret")})
	if err != nil {
		t.Fatal(err)
	}
	err = r.Exists(context.Background())
	if err == nil {
		t.Fatal("a redirect to another site must fail")
	}
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Status != 502 || !strings.Contains(pe.Message, "Redmine redirected to another site") {
		t.Fatalf("want a 502 'Redmine redirected to another site', got %v", err)
	}
	if n := leaked.Load(); n != 0 {
		t.Fatalf("the key reached the other site %d time(s)", n)
	}
}

// A 307 replays the body of a POST, so a POST redirected to another site must not send its body there either.
func TestAPostRedirectedWith307DoesNotReplayTheBodyElsewhere(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		respond(w, 201, map[string]any{})
	}))
	t.Cleanup(other.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)

	r, _ := New("redmine:"+first.URL+"/backend", Options{Key: keyOf("secret")})
	c := r.newClient("secret")
	if err := c.write(context.Background(), http.MethodPost, "/issues.json", map[string]any{"issue": map[string]any{"subject": "x"}}, nil); err == nil {
		t.Fatal("a POST redirected to another site must fail")
	}
	if n := reached.Load(); n != 0 {
		t.Fatalf("the POST reached the other site %d time(s)", n)
	}
}

// A redirect on the same origin is still followed.
func TestASameOriginRedirectIsFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend/old" {
			http.Redirect(w, r, "/backend/new", http.StatusFound)
			return
		}
		if r.Header.Get("X-Redmine-API-Key") != "secret" {
			respond(w, 401, nil)
			return
		}
		respond(w, 200, map[string]any{"project": map[string]any{"id": 1, "identifier": "backend"}})
	}))
	t.Cleanup(srv.Close)
	r, _ := New("redmine:"+srv.URL+"/backend", Options{Key: keyOf("secret")})
	c := r.newClient("secret")
	var d struct {
		Project rmProject `json:"project"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/old", nil, "", nil, &d); err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
}
