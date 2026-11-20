package github

import (
	"context"
	"net/http"
	"testing"
)

func TestAPostAnsweredWith500IsSentExactlyOnce(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})
	r, _ := testRepo(t, h)
	err := r.api.rest(context.Background(), "POST", r.repoURL()+"/issues/7/comments", "t", map[string]any{"body": "x"}, nil)
	if err == nil {
		t.Fatal("a 500 is an error")
	}
	if calls != 1 {
		t.Fatalf("a POST answered 500 was sent %d times, want 1", calls)
	}
}

func TestAGetAnsweredWith500IsRetried(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	r, _ := testRepo(t, h)
	var out struct {
		ID int `json:"id"`
	}
	if err := r.api.rest(context.Background(), "GET", r.repoURL()+"/issues/7", "t", nil, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || out.ID != 1 {
		t.Fatalf("a GET answered 500 then 200: calls=%d id=%d", calls, out.ID)
	}
}

func TestA429IsRetriedForAPost(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(201)
	})
	r, _ := testRepo(t, h)
	if err := r.api.rest(context.Background(), "POST", r.repoURL()+"/issues", "t", map[string]any{"title": "x"}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("a 429 is retried even for a POST: calls=%d", calls)
	}
}

func TestRestAllFirstPageErrorComesFromTheAnswerAlreadyRead(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	r, _ := testRepo(t, h)
	_, err := restAll[restLabel](context.Background(), r.api, r.repoURL()+"/labels", "t", 5)
	if err == nil {
		t.Fatal("a 404 on the first page is an error")
	}
	if calls != 1 {
		t.Fatalf("the first page was sent %d times, want 1", calls)
	}
}
