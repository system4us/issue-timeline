package jira

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The client that carries the Basic credential refuses a redirect to another site, so the credential stays here.
func TestTheBasicCredentialIsNotSentToAnotherSiteOnARedirect(t *testing.T) {
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

	r, err := New("jira:"+first.URL+"/PROJ", Options{Creds: creds})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, first.URL+"/rest/api/2/search", nil)
	req.Header.Set("Authorization", "Basic c2VjcmV0")
	if resp, err := r.http.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("a redirect to another site must fail")
	}
	if n := leaked.Load(); n != 0 {
		t.Fatalf("the credential reached the other site %d time(s)", n)
	}
}
