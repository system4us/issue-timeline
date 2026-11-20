package access

import (
	"net/http"
	"net/url"
	"strings"
)

// WriteHeader is the header every write must carry. A browser only sends a custom header from this app's own code (a
// custom header needs a CORS preflight, which is never granted), so a page from another site cannot write.
const WriteHeader = "x-issue-timeline"

// WriteRefusal is "" when the request may write; otherwise the reason it is refused. Reads are never refused. A write
// needs the header, and an Origin header, when the browser sends one, must be the origin the request was made to (its
// Host), so a rebound DNS name cannot pass as its own.
func WriteRefusal(r *http.Request) string {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return ""
	}
	if r.Header.Get(WriteHeader) != "1" {
		return "a write needs the x-issue-timeline header (the app sends it itself)"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return ""
	}
	const another = "the write comes from another origin"
	u, err := url.Parse(origin)
	if err != nil {
		return another
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" {
		return another
	}
	if originHost(scheme, u) != strings.ToLower(requestHost(r)) {
		return another
	}
	return ""
}

// originHost is the host of an origin as a browser writes it: lower case, without its scheme's default port (Node's
// URL drops ":80" and ":443" the same way).
func originHost(scheme string, u *url.URL) string {
	name := strings.ToLower(u.Hostname())
	if strings.Contains(name, ":") {
		name = "[" + name + "]"
	}
	port := u.Port()
	if port == "" || (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		return name
	}
	return name + ":" + port
}
