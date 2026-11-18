// Package access answers who a request comes from and whether this server answers it at all.
//
//   - The Host header must name this server (a loopback name, the bind address, this machine's own names and addresses,
//     or one of ALLOWED_HOSTS). That stops DNS rebinding: a page on another site whose name resolves to 127.0.0.1 sends
//     its own name as the Host, and is refused, so it cannot read the API through the browser.
//   - A request is local when it comes from this machine's loopback without any proxy header. A local request without a
//     session uses this machine's own identity (gh, glab, the stored secrets) and may change the configuration. A reverse
//     proxy on the same machine must set X-Forwarded-For (or Forwarded / X-Real-IP), or every request through it is local.
package access

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
)

// proxyHeaders are the headers a reverse proxy adds; a request carrying one is not local.
var proxyHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip"}

// IsLocalRequest is true when the request comes straight from this machine: a loopback address and no proxy header.
func IsLocalRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	for _, h := range proxyHeaders {
		// present, even when empty, counts as a proxy: the JS checks for the header, not its value
		if _, ok := r.Header[h]; ok {
			return false
		}
	}
	return true
}

// AllowedHostNames are the host names (without port) this server answers to, given the address it is bound to. Bound
// to every interface (0.0.0.0 or ::), the machine's own name and addresses are added. ALLOWED_HOSTS (from env) adds
// more. Names are lower case; an IPv6 address is in brackets.
func AllowedHostNames(bindHost string, env func(string) string) map[string]bool {
	if env == nil {
		env = os.Getenv
	}
	names := map[string]bool{"localhost": true, "127.0.0.1": true, "[::1]": true}
	add := func(h string) {
		if h == "" {
			return
		}
		h = strings.ToLower(h)
		if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
			h = "[" + h + "]"
		}
		names[h] = true
	}
	if bindHost == "0.0.0.0" || bindHost == "::" {
		// bound to every interface: this machine's own names and addresses
		if name, err := os.Hostname(); err == nil {
			add(name)
		}
		if ifaces, err := net.Interfaces(); err == nil {
			for _, ifc := range ifaces {
				addrs, err := ifc.Addrs()
				if err != nil {
					continue
				}
				for _, a := range addrs {
					if ipn, ok := a.(*net.IPNet); ok {
						add(ipn.IP.String())
					}
				}
			}
		}
	} else {
		add(bindHost)
	}
	for _, h := range strings.Split(env("ALLOWED_HOSTS"), ",") {
		add(strings.TrimSpace(h))
	}
	return names
}

// HostNameOf is the host name of a Host header, without its port ("[::1]:4310" gives "[::1]", "a.b:80" gives "a.b").
// It is "" when the header is empty, or an IPv6 literal has no closing bracket.
func HostNameOf(header string) string {
	h := strings.ToLower(strings.TrimSpace(header))
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") {
		i := strings.Index(h, "]")
		if i < 0 {
			return ""
		}
		return h[:i+1]
	}
	name, _, _ := strings.Cut(h, ":")
	return name
}

// requestHost is the Host of a request: r.Host, which the server fills from the header.
func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.Header.Get("Host")
}

// HostRefusal is "" when the Host header names this server (is in allowed); otherwise the reason it is refused.
func HostRefusal(r *http.Request, allowed map[string]bool) string {
	name := HostNameOf(requestHost(r))
	if name == "" {
		return "the request has no Host header"
	}
	if allowed[name] {
		return ""
	}
	return fmt.Sprintf("this server does not answer to the host %s (add it to ALLOWED_HOSTS)", name)
}

// HTTPError is an error with the HTTP status the API answers with.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return e.Message }

// ReadJSON reads a request body into v, as a JSON object, at most limit bytes. An empty body is {}. A body that is not a
// JSON object is a 400; one over the limit is a 413. A value that does not fit v's fields is a 400 too.
func ReadJSON(r *http.Request, limit int64, v any) error {
	var body io.ReadCloser = http.NoBody
	if r.Body != nil {
		// a nil ResponseWriter: the server is not told to close the connection, the reader only reports the size
		body = http.MaxBytesReader(nil, r.Body, limit)
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &HTTPError{Status: 413, Message: fmt.Sprintf("the request body is larger than %s MB", formatMB(limit))}
		}
		return fmt.Errorf("reading the request body: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	// null and arrays decode without error into a map, so the object check is the map's own check
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return &HTTPError{Status: 400, Message: "the request body is not a JSON object"}
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &HTTPError{Status: 400, Message: "the request body does not fit the expected fields: " + err.Error()}
	}
	return nil
}

// formatMB is a byte count in whole megabytes, rounded as the JS does (Math.round).
func formatMB(n int64) string {
	return fmt.Sprintf("%g", math.Round(float64(n)/1024/1024))
}
