package access

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// write is a request with method, Host and the headers given (name, value pairs).
func write(method, host string, headers ...string) *http.Request {
	r := httptest.NewRequest(method, "/", nil)
	r.Host = host
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func TestReadsAreNeverRefused(t *testing.T) {
	if got := WriteRefusal(write(http.MethodGet, "127.0.0.1:4310")); got != "" {
		t.Fatalf("GET refused: %s", got)
	}
}

func TestAWriteWithoutTheAppsHeaderIsRefusedWhateverItsOrigin(t *testing.T) {
	for _, r := range []*http.Request{
		write(http.MethodPost, "127.0.0.1:4310"),
		write(http.MethodPatch, "127.0.0.1:4310", "Origin", "http://127.0.0.1:4310"),
	} {
		if got := WriteRefusal(r); !strings.Contains(got, "x-issue-timeline header") {
			t.Fatalf("%s: %q", r.Method, got)
		}
	}
}

func TestAWriteWithTheHeaderFromItsOwnOriginIsAllowedAndFromAnotherIsRefused(t *testing.T) {
	const h = "127.0.0.1:4310"
	if got := WriteRefusal(write(http.MethodPatch, h, WriteHeader, "1", "Origin", "http://127.0.0.1:4310")); got != "" {
		t.Fatalf("own origin refused: %s", got)
	}
	if got := WriteRefusal(write(http.MethodPost, h, WriteHeader, "1")); got != "" {
		t.Fatalf("no Origin (the app's own fetch) refused: %s", got)
	}
	for _, origin := range []string{"https://evil.example", "null"} {
		if got := WriteRefusal(write(http.MethodPost, h, WriteHeader, "1", "Origin", origin)); !strings.Contains(got, "another origin") {
			t.Fatalf("%s: %q", origin, got)
		}
	}
}

func TestTheServerReachedByItsLANAddressOrIPv6LoopbackAcceptsItsOwnPagesWrites(t *testing.T) {
	ok := []*http.Request{
		write(http.MethodPost, "192.168.1.5:4310", WriteHeader, "1", "Origin", "http://192.168.1.5:4310"),
		write(http.MethodPost, "[::1]:4310", WriteHeader, "1", "Origin", "http://[::1]:4310"),
		write(http.MethodPost, "timeline.example.com", WriteHeader, "1", "Origin", "https://timeline.example.com"),
		// the default port is dropped by a browser's Origin, as by Node's URL
		write(http.MethodPost, "timeline.example.com", WriteHeader, "1", "Origin", "https://timeline.example.com:443"),
		write(http.MethodPost, "Timeline.Example.com", WriteHeader, "1", "Origin", "https://timeline.example.com"),
	}
	for _, r := range ok {
		if got := WriteRefusal(r); got != "" {
			t.Errorf("%s %s refused: %s", r.Host, r.Header.Get("Origin"), got)
		}
	}
	if got := WriteRefusal(write(http.MethodPost, "192.168.1.5:4310", WriteHeader, "1", "Origin", "http://192.168.1.5:9999")); !strings.Contains(got, "another origin") {
		t.Fatalf("other port: %q", got)
	}
}
