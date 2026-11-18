package jira

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestAGetAnsweredWithAServerErrorIsRetried(t *testing.T) {
	var tries atomic.Int32
	r := newRepo(t, func(w http.ResponseWriter, req *http.Request) {
		if tries.Add(1) == 1 {
			respond(w, 500, nil)
			return
		}
		respond(w, 200, map[string]any{"id": "1", "key": "PROJ", "name": "P"})
	})
	var ws waits
	r.sleep = ws.sleep
	if err := r.Exists(context.Background()); err != nil {
		t.Fatalf("the second try should read: %v", err)
	}
	if n := tries.Load(); n != 2 {
		t.Fatalf("tries: %d", n)
	}
}

func TestAZeroRetryAfterWaitsTheBackoff(t *testing.T) {
	var tries atomic.Int32
	r := newRepo(t, func(w http.ResponseWriter, req *http.Request) {
		if tries.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			respond(w, 429, nil)
			return
		}
		respond(w, 200, map[string]any{"id": "1", "key": "PROJ", "name": "P"})
	})
	var ws waits
	r.sleep = ws.sleep
	if err := r.Exists(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := ws.all(); len(d) != 1 || d[0] != 2*time.Second {
		t.Fatalf("a zero Retry-After waits 2 s: %v", d)
	}
}
