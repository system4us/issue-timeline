package github

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The client that carries the token refuses a redirect to another site, so the token stays here.
func TestTheTokenIsNotSentToAnotherSiteOnARedirect(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
	}))
	t.Cleanup(other.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(first.Close)

	a := newAPI(Options{})
	req, _ := http.NewRequest(http.MethodGet, first.URL+"/repos/o/r", nil)
	req.Header.Set("authorization", "Bearer secret")
	if resp, err := a.http.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("a redirect to another site must fail")
	}
	if n := leaked.Load(); n != 0 {
		t.Fatalf("the token reached the other site %d time(s)", n)
	}
}
