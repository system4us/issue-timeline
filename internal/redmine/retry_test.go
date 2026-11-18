package redmine

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// waits records the sleeps between tries instead of sleeping.
type waits struct {
	mu sync.Mutex
	ds []time.Duration
}

func (w *waits) sleep(ctx context.Context, d time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ds = append(w.ds, d)
	return ctx.Err()
}

func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.ds...)
}

func TestAPostAnsweredWithAServerErrorIsSentOnce(t *testing.T) {
	var posts atomic.Int32
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/issues.json":
			posts.Add(1)
			respond(w, 500, nil)
		case r.URL.Path == "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		default:
			respond(w, 404, nil)
		}
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	var ws waits
	r.sleep = ws.sleep
	_, err := r.CreateIssue(context.Background(), provider.Cred{Token: "k"}, provider.Changes{"title": []byte(`"New thing"`)})
	if err == nil {
		t.Fatal("a server error on a create is an error for the caller")
	}
	if n := posts.Load(); n != 1 {
		t.Fatalf("the POST was sent %d times; it must be sent once", n)
	}
	if len(ws.all()) != 0 {
		t.Fatalf("a POST is not retried, so nothing waits: %v", ws.all())
	}
}

func TestAPostBusyWithARateLimitIsRetried(t *testing.T) {
	var posts atomic.Int32
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/issues.json":
			if posts.Add(1) == 1 {
				w.Header().Set("Retry-After", "3")
				respond(w, 429, nil)
				return
			}
			respond(w, 201, map[string]any{"issue": map[string]any{"id": 77}})
		case r.URL.Path == "/projects/backend.json":
			respond(w, 200, map[string]any{"project": map[string]any{"id": 42}})
		case r.URL.Path == "/issue_statuses.json":
			respond(w, 200, map[string]any{"issue_statuses": []any{}})
		case r.URL.Path == "/custom_fields.json":
			respond(w, 403, nil)
		case r.URL.Path == "/issues/77.json":
			respond(w, 200, map[string]any{"issue": rawIssue(77, nil)})
		default:
			respond(w, 404, nil)
		}
	})
	r, _ := New(key, Options{Key: keyOf("k")})
	var ws waits
	r.sleep = ws.sleep
	if _, err := r.CreateIssue(context.Background(), provider.Cred{Token: "k"}, provider.Changes{"title": []byte(`"New thing"`)}); err != nil {
		t.Fatalf("a rate limit is safe to ask again: %v", err)
	}
	if n := posts.Load(); n != 2 {
		t.Fatalf("POST tries: %d", n)
	}
	if d := ws.all(); len(d) != 1 || d[0] != 3*time.Second {
		t.Fatalf("Retry-After of 3 s is waited: %v", d)
	}
}

func TestAGetAnsweredWithAServerErrorIsRetried(t *testing.T) {
	var tries atomic.Int32
	key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
		if tries.Add(1) == 1 {
			respond(w, 500, nil)
			return
		}
		respond(w, 200, map[string]any{"user": map[string]any{"id": 7, "login": "ana"}})
	})
	r, _ := New(key, Options{})
	var ws waits
	r.sleep = ws.sleep
	if _, err := r.WhoAmI(context.Background(), "k"); err != nil {
		t.Fatalf("the second try should read: %v", err)
	}
	if n := tries.Load(); n != 2 {
		t.Fatalf("tries: %d", n)
	}
}

func TestAMissingOrZeroRetryAfterWaitsTheBackoff(t *testing.T) {
	for _, header := range []string{"", "0"} {
		var tries atomic.Int32
		key := fakeRedmine(t, func(w http.ResponseWriter, r *http.Request) {
			if tries.Add(1) == 1 {
				if header != "" {
					w.Header().Set("Retry-After", header)
				}
				respond(w, 429, nil)
				return
			}
			respond(w, 200, map[string]any{"user": map[string]any{"id": 7, "login": "ana"}})
		})
		r, _ := New(key, Options{})
		var ws waits
		r.sleep = ws.sleep
		if _, err := r.WhoAmI(context.Background(), "k"); err != nil {
			t.Fatal(err)
		}
		if d := ws.all(); len(d) != 1 || d[0] != 2*time.Second {
			t.Fatalf("Retry-After %q: the first wait is 2 s, got %v", header, d)
		}
	}
}
