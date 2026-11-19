package access

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// req is a request from remoteAddr with the headers given.
func req(remoteAddr string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func TestARequestIsLocalOnlyFromLoopbackAndWithoutAProxyHeader(t *testing.T) {
	cases := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"ipv4 loopback", req("127.0.0.1:5000"), true},
		{"ipv6 loopback", req("[::1]:5000"), true},
		{"v4-mapped loopback", req("[::ffff:127.0.0.1]:5000"), true},
		{"another 127 address", req("127.9.9.9:5000"), true},
		{"a LAN address", req("192.168.1.9:5000"), false},
		{"no port", req("127.0.0.1"), false},
		{"a proxy on this machine (X-Forwarded-For)", req("127.0.0.1:5000", "X-Forwarded-For", "203.0.113.4"), false},
		{"Forwarded", req("127.0.0.1:5000", "Forwarded", "for=203.0.113.4"), false},
		{"X-Real-IP", req("127.0.0.1:5000", "X-Real-IP", "203.0.113.4"), false},
		{"an empty proxy header still counts", req("127.0.0.1:5000", "X-Forwarded-For", ""), false},
	}
	for _, c := range cases {
		if got := IsLocalRequest(c.r); got != c.want {
			t.Errorf("%s: IsLocalRequest = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTheHostHeaderMustNameThisServerARebouncedNameIsRefused(t *testing.T) {
	allowed := AllowedHostNames("127.0.0.1", func(string) string { return "" })
	host := func(h string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = h
		return r
	}
	for _, h := range []string{"127.0.0.1:4310", "localhost:4310", "[::1]:4310"} {
		if got := HostRefusal(host(h), allowed); got != "" {
			t.Errorf("%s refused: %s", h, got)
		}
	}
	if got := HostRefusal(host("evil.example:4310"), allowed); !strings.Contains(got, "does not answer") {
		t.Errorf("evil.example: %q", got)
	}
	noHost := httptest.NewRequest(http.MethodGet, "/", nil)
	noHost.Host = ""
	noHost.Header.Del("Host")
	if got := HostRefusal(noHost, allowed); !strings.Contains(got, "no Host") {
		t.Errorf("no Host: %q", got)
	}
}

func TestBoundToEveryInterfaceTheMachinesOwnNamesAreAllowedAndAllowedHostsAddsMore(t *testing.T) {
	env := func(k string) string {
		if k == "ALLOWED_HOSTS" {
			return "timeline.example.com, Other.Example"
		}
		return ""
	}
	allowed := AllowedHostNames("0.0.0.0", env)
	// this machine's own names and addresses are whatever the host has: the ones the test can rely on are these
	for _, h := range []string{"timeline.example.com", "other.example", "localhost", "127.0.0.1", "[::1]"} {
		if !allowed[h] {
			t.Errorf("%s not allowed", h)
		}
	}
	if allowed["0.0.0.0"] {
		t.Error("the bind address 0.0.0.0 is a name a request can carry")
	}
	if HostNameOf("[::1]:4310") != "[::1]" || HostNameOf("Box:80") != "box" || HostNameOf("") != "" || HostNameOf("[::1") != "" {
		t.Error("HostNameOf")
	}
}

func TestAnIPv6BindAddressIsAddedInBrackets(t *testing.T) {
	allowed := AllowedHostNames("fe80::1", func(string) string { return "" })
	if !allowed["[fe80::1]"] {
		t.Fatalf("allowed = %v", allowed)
	}
}

func TestAJSONObjectIsReadFromTheBodyWithinALimit(t *testing.T) {
	body := func(text string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(text))
		return r
	}
	var v map[string]any
	if err := ReadJSON(body(`{"a":1}`), 1<<20, &v); err != nil || v["a"] != float64(1) {
		t.Fatalf("object: %v, %v", v, err)
	}
	v = nil
	if err := ReadJSON(body(""), 1<<20, &v); err != nil || v == nil || len(v) != 0 {
		t.Fatalf("empty body is {}: %v, %v", v, err)
	}
	for _, bad := range []string{"{nope", "[1,2]", "null", "7", `"x"`} {
		var m map[string]any
		err := ReadJSON(body(bad), 1<<20, &m)
		var he *HTTPError
		if !errors.As(err, &he) || he.Status != 400 {
			t.Errorf("%q: err = %v, want a 400", bad, err)
		}
	}
	var m map[string]any
	err := ReadJSON(body(strings.Repeat("x", 2000)), 1000, &m)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 413 {
		t.Fatalf("over the limit: err = %v, want a 413", err)
	}
	if he.Message != "the request body is larger than 0 MB" {
		t.Fatalf("message = %q", he.Message)
	}
}

func TestAJSONBodyThatDoesNotFitTheTargetIsA400(t *testing.T) {
	type target struct {
		N int `json:"n"`
	}
	var tg target
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"n":"text"}`))
	var he *HTTPError
	if err := ReadJSON(r, 1<<20, &tg); !errors.As(err, &he) || he.Status != 400 {
		t.Fatalf("err = %v, want a 400", err)
	}
	// a body with no Body at all is an empty object
	r2 := &http.Request{Header: http.Header{}}
	var m map[string]any
	if err := ReadJSON(r2, 1<<20, &m); err != nil {
		t.Fatalf("nil body: %v", err)
	}
}
