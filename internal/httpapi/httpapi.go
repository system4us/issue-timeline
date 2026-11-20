// Package httpapi turns HTTP requests into calls of internal/app. It holds the gates every request passes (the Host
// check, the write check, the session, the local identity), the routes of api/openapi.yaml, the visibility each
// signed-in person gets, and the static web app.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime/debug"
	"strings"
	"sync"

	"gitlab.com/system4us/issue-timeline/internal/access"
	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	itmcp "gitlab.com/system4us/issue-timeline/internal/mcp"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// Options are what the server is started with (see cmd/issue-timeline).
type Options struct {
	BindHost string               // the address the server listens on: the Host names it answers to are derived from it
	Port     int                  // the port it listens on (for the startup message; the handler does not use it)
	Env      func(string) string  // os.Getenv in production: ALLOWED_HOSTS and the session limits are read from it
	Sessions *auth.Sessions       // the signed-in people; nil means a new set with the defaults
	UI       fs.FS                // the built web app; nil means every page answers "the frontend is not built"
	Log      func(string, ...any) // where panics are logged; nil means the standard logger
	Context  context.Context      // when non-nil, starts proactive background OAuth token renewal until it is cancelled
}

// server is the handler: the app, the gates' settings and the routes.
type server struct {
	app      *app.App
	sessions *auth.Sessions
	allowed  map[string]bool // the Host names this server answers to, computed once
	ui       fs.FS
	log      func(string, ...any)
	mux      *http.ServeMux
	mcp      http.Handler
	// hookSecret is WEBHOOK_SECRET: the one secret every host's webhook deliveries are checked against ("" turns
	// POST /api/webhook off).
	hookSecret string
	env        func(string) string // the environment: the OAuth apps and PUBLIC_URL are read from it when asked for
	flows      flows               // the OAuth sign-ins under way
	renewMu    sync.Mutex          // one OAuth token renewal at a time
}

// New returns the handler of the API and the web app. a is the running app (see app.New).
func New(a *app.App, o Options) http.Handler {
	env := o.Env
	if env == nil {
		env = os.Getenv
	}
	s := &server{
		app:      a,
		sessions: o.Sessions,
		allowed:  access.AllowedHostNames(o.BindHost, env),
		ui:       o.UI,
		log:      o.Log,

		hookSecret: env("WEBHOOK_SECRET"),
		env:        env,
	}
	if s.sessions == nil {
		s.sessions = auth.NewSessions(auth.SessionOptions{Env: env})
	}
	if s.log == nil {
		s.log = log.Printf
	}
	if a != nil {
		s.mcp = itmcp.NewHandler(a, func(ctx context.Context) (app.Who, bool) {
			c, ok := ctx.Value(callerKey{}).(caller)
			if !ok || (c.sess == nil && !c.local) {
				return app.Who{}, false
			}
			return c.who(), true
		})
		a.SetFallbackToken(func(ctx context.Context, prov, origin string) (string, error) {
			sess := s.renewed(ctx, s.sessions.LatestFor(prov, origin))
			if sess == nil {
				return "", errors.New("no active session")
			}
			acct, ok := sess.AccountFor(prov, origin)
			if !ok {
				return "", errors.New("no active session")
			}
			return acct.Token, nil
		})
	}
	s.mux = s.routes()
	if o.Context != nil {
		go s.renewLoop(o.Context, renewCheckInterval)
	}
	return s
}

// caller is who a request comes from: its session (nil for this machine's own identity) and whether it is local.
type caller struct {
	sess  *auth.Session
	local bool
}

// who is the app's view of the caller.
func (c caller) who() app.Who { return app.Who{Session: c.sess, Local: c.local} }

type callerKey struct{}

// callerOf is the caller a request was checked for (set by ServeHTTP).
func callerOf(r *http.Request) caller {
	c, _ := r.Context().Value(callerKey{}).(caller)
	return c
}

// openPaths are the routes that answer without a session from a request that is not local. Each one checks its own
// input, and none of them shows a record.
var openPaths = map[string]bool{
	"/api/me":              true,
	"/api/session":         true,
	"/api/openapi.yaml":    true,
	"/api/docs":            true,
	webhookPath:            true, // a host's delivery: its signature is its check
	"/api/sign-in-options": true,
}

// ServeHTTP runs the gates in the order the server always has: the Host, the write header and Origin, the session, and
// the 401 for an API route that needs a session. Then the route answers. A panic is logged and answered as 500.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			s.log("httpapi: %s %s: panic: %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
			writeError(w, http.StatusInternalServerError, "unexpected error on the server")
		}
	}()
	if msg := access.HostRefusal(r, s.allowed); msg != "" {
		writeError(w, http.StatusMisdirectedRequest, msg)
		return
	}
	if r.URL.Path == "/mcp" {
		s.serveMCP(w, r)
		return
	}
	// a webhook delivery carries no write header and no Origin (a host sends it, not the page): its signature is checked
	if msg := access.WriteRefusal(r); msg != "" && !(r.Method == http.MethodPost && r.URL.Path == webhookPath) {
		writeError(w, http.StatusForbidden, msg)
		return
	}
	// an OAuth sign-in's token is renewed here, before it expires, so every route sees a working one
	sess := s.renewed(r.Context(), s.sessions.Get(auth.SessionIDOf(r)))
	local := access.IsLocalRequest(r)
	open := openPaths[r.URL.Path] || strings.HasPrefix(r.URL.Path, oauthPrefix) // a sign-in under way has no session yet
	if strings.HasPrefix(r.URL.Path, "/api/") && !open && sess == nil && !local {
		writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller{sess: sess, local: local}))
	s.mux.ServeHTTP(w, r)
}

// serveMCP authenticates MCP clients via Bearer token (or non-browser local loopback) and delegates to the read-only MCP handler.
// writeExpiredMCPToken answers a token whose host sign-in was revoked, saying how to recover.
func writeExpiredMCPToken(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", error_description="the host sign-in behind this MCP token was revoked"`)
	writeError(w, http.StatusUnauthorized, "MCP token expired: the host sign-in it was created from was revoked. Sign in again, revoke this token and create a new one")
}

func (s *server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeError(w, http.StatusNotFound, "MCP server unavailable")
		return
	}
	if tok := auth.BearerToken(r); tok != "" {
		sess, mcpLocal, ok := s.sessions.AuthenticateMCP(tok)
		if !ok {
			if s.sessions.MCPTokenExpired(tok) {
				writeExpiredMCPToken(w)
				return
			}
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "invalid MCP token")
			return
		}
		if sess != nil {
			sess = s.renewed(r.Context(), sess)
			if sess == nil {
				writeExpiredMCPToken(w)
				return
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller{sess: sess, local: mcpLocal}))
		s.mcp.ServeHTTP(w, r)
		return
	}
	if access.IsLocalRequest(r) && r.Header.Get("Origin") == "" {
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller{local: true}))
		s.mcp.ServeHTTP(w, r)
		return
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "send Authorization: Bearer <mcp_token>")
}

// writeJSON answers with a JSON body. Every JSON answer is not cached.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with the API's error body, {"error": msg}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusOf is the status an error answers with. An HTTP error and a host error carry their own status; a host that
// does not have an operation is 501; a record the host does not show is 404. Any other error gets plain, the status
// of the kind of operation (502 for one that talks to a host, 500 for one that does not).
func statusOf(err error, plain int) (int, string) {
	var he *access.HTTPError
	if errors.As(err, &he) && he.Status != 0 {
		return he.Status, err.Error()
	}
	var pe *provider.Error
	if errors.As(err, &pe) && pe.Status != 0 {
		return pe.Status, err.Error()
	}
	switch {
	case errors.Is(err, provider.ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, provider.ErrNotSupported):
		return http.StatusNotImplemented, err.Error()
	}
	return plain, err.Error()
}

// fail answers an error an operation returned (see statusOf).
func fail(w http.ResponseWriter, err error, plain int) {
	status, msg := statusOf(err, plain)
	writeError(w, status, msg)
}

// readBody reads a JSON object of at most limit bytes into v. The error answers as the access package says (400, 413).
func readBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := access.ReadJSON(r, limit, v); err != nil {
		fail(w, err, http.StatusBadRequest)
		return false
	}
	return true
}

const (
	bodyLimit      = 1 << 20  // 1 MB
	signInLimit    = 64 << 10 // 64 KB
	issueEditLimit = 40 << 20 // 40 MB: attachments come in the body, base64
	maxAttachment  = 5 << 20  // 5 MB decoded, per file
	maxAttachments = 5
)

// mimeTypes are the content types the web app's files are served with (the types of the Node server).
var mimeTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript",
	".css":  "text/css",
	".svg":  "image/svg+xml",
	".json": "application/json",
}

// static serves the web app: the file at the path, else index.html (the page is a single-page app), else a 404 that
// says the frontend is not built. Nothing outside the web app's tree is ever read.
func (s *server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if s.serveFile(w, name) || s.serveFile(w, "index.html") {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "The frontend is not built: run `make ui` (or `npm run build` in web/), then build the server again.")
}

// serveFile writes the file at name of the web app. It is false when there is no such file (or it is a folder).
func (s *server) serveFile(w http.ResponseWriter, name string) bool {
	if s.ui == nil || !fs.ValidPath(name) {
		return false
	}
	f, err := s.ui.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	ct, ok := mimeTypes[path.Ext(name)]
	if !ok {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write(body)
	return true
}

// providerOf is the host a repo key belongs to: "redmine" and "jira" and "gitlab" by their prefix, GitHub otherwise.
func providerOf(repo string) string {
	switch {
	case strings.HasPrefix(repo, "redmine:"):
		return "redmine"
	case strings.HasPrefix(repo, "jira:"):
		return "jira"
	case strings.HasPrefix(repo, "gitlab:"):
		return "gitlab"
	}
	return "github"
}

// nonNil turns a nil slice into an empty one, so the JSON has [] and not null.
func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// base64Size is how many bytes a base64 text decodes to (as Node's Buffer.byteLength counts them).
func base64Size(s string) int {
	pad := 0
	switch {
	case strings.HasSuffix(s, "=="):
		pad = 2
	case strings.HasSuffix(s, "="):
		pad = 1
	}
	return (len(s) - pad) * 3 / 4
}

// originOf is the origin (scheme and host, without a default port) of a site URL.
func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("not a site URL: %q", raw)
	}
	scheme, host, port := strings.ToLower(u.Scheme), strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" && !((scheme == "https" && port == "443") || (scheme == "http" && port == "80")) {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}
