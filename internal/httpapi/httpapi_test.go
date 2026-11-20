package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"gitlab.com/system4us/issue-timeline/internal/auth"
)

// opts are the server's options for the tests: bound to loopback, no environment, the given web app.
func opts(ui fstest.MapFS) Options {
	return Options{
		BindHost: "127.0.0.1",
		Port:     4310,
		Env:      func(string) string { return "" },
		Sessions: auth.NewSessions(auth.SessionOptions{}),
		UI:       ui,
		Log:      func(string, ...any) {},
	}
}

// serve runs one request through the handler and returns the response.
func serve(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// assertJSONError checks a refusal: its status, a JSON body with an error, and the answer's headers.
func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content type %q, want application/json; charset=utf-8", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control %q, want no-store", cc)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("body %q has no error", rec.Body.String())
	}
}

// The gates run before any route. The app is built for real, as in production.
func TestHostNotThisServerIs421(t *testing.T) {
	h := New(buildApp(t), opts(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/repos", nil)
	req.Host = "evil.example"
	assertJSONError(t, serve(t, h, req), http.StatusMisdirectedRequest)
}

func TestForeignOriginOnWriteIs403(t *testing.T) {
	h := New(buildApp(t), opts(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:4310"
	req.Header.Set("x-issue-timeline", "1")
	req.Header.Set("Origin", "http://evil.example")
	assertJSONError(t, serve(t, h, req), http.StatusForbidden)
}

func TestWriteWithoutHeaderIs403(t *testing.T) {
	h := New(buildApp(t), opts(nil))
	req := httptest.NewRequest(http.MethodPost, "/api/links", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "127.0.0.1:40000"
	assertJSONError(t, serve(t, h, req), http.StatusForbidden)
}

func TestRemoteWithoutSessionIs401(t *testing.T) {
	h := New(buildApp(t), opts(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/data?repo=owner/name", nil)
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "192.0.2.10:5555"
	assertJSONError(t, serve(t, h, req), http.StatusUnauthorized)
}

// Without a session the open routes still answer: the spec and the docs page need none.
func TestOpenRoutesAnswerWithoutSession(t *testing.T) {
	h := New(buildApp(t), opts(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "192.0.2.10:5555"
	rec := serve(t, h, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "openapi: 3.1.0") {
		t.Fatalf("spec: status %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/docs", nil)
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "192.0.2.10:5555"
	rec = serve(t, h, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("docs: status %d, CSP %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
}

func TestMeWithoutSessionIs401(t *testing.T) {
	h := New(buildApp(t), opts(nil))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "127.0.0.1:40000"
	rec := serve(t, h, req)
	assertJSONError(t, rec, http.StatusUnauthorized)
	if !strings.Contains(rec.Body.String(), "not signed in") {
		t.Errorf("body %q, want not signed in", rec.Body.String())
	}
}

func TestStaticFallsBackToIndexAndNeverLeavesTheWebApp(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":   {Data: []byte("<!doctype html>app")},
		"app.js":       {Data: []byte("console.log(1)")},
		"secret.txt":   {Data: []byte("outside")},
		"assets/a.css": {Data: []byte("a{}")},
	}
	h := New(buildApp(t), opts(ui))
	for _, c := range []struct{ path, body, ct string }{
		{"/", "<!doctype html>app", "text/html; charset=utf-8"},
		{"/app.js", "console.log(1)", "text/javascript"},
		{"/assets/a.css", "a{}", "text/css"},
		{"/some/page", "<!doctype html>app", "text/html; charset=utf-8"},
	} {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Host = "127.0.0.1:4310"
		rec := serve(t, h, req)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != http.StatusOK || string(body) != c.body || rec.Header().Get("Content-Type") != c.ct {
			t.Errorf("%s: status %d, type %q, body %q; want %q, %q", c.path, rec.Code, rec.Header().Get("Content-Type"), body, c.body, c.ct)
		}
	}
}

func TestStaticWithoutAnUIIsNotBuilt(t *testing.T) {
	h := New(buildApp(t), opts(nil))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:4310"
	rec := serve(t, h, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "The frontend is not built") {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
}

// A path that climbs out of the web app is cleaned to a path inside it (or redirected by the router): the web app is
// an fs.FS, which cannot name a file outside its root, so such a path gets the index page, never an error.
func TestStaticPathsThatClimbGetTheIndex(t *testing.T) {
	ui := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>app")}}
	h := New(buildApp(t), opts(ui))
	for _, p := range []string{"/../secret.txt", "/%2e%2e/secret.txt", "/a/../../secret.txt"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = "127.0.0.1:4310"
		rec := serve(t, h, req)
		if rec.Code == http.StatusInternalServerError || rec.Code == http.StatusNotFound {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
}
