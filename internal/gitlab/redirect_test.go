package gitlab

import (
	"gitlab.com/system4us/issue-timeline/internal/model"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The client that carries the token refuses a redirect to another site, so the token stays here.
func TestTheTokenIsNotSentToAnotherSiteOnARedirect(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("PRIVATE-TOKEN") != "" {
			leaked.Add(1)
		}
	}))
	t.Cleanup(other.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)

	r, err := New("gitlab:"+first.URL+"/group/project", Options{Categorize: func(model.CIRun) string { return "ci" }})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, first.URL+"/api/v4/user", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("PRIVATE-TOKEN", "secret")
	if resp, err := r.http.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("a redirect to another site must fail")
	}
	if n := leaked.Load(); n != 0 {
		t.Fatalf("the token reached the other site %d time(s)", n)
	}
}
