package provider

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestSameOriginRedirectsRefusesAnotherSiteOrScheme(t *testing.T) {
	from, _ := url.Parse("https://a.example/api/x")
	orig := &http.Request{URL: from}
	cases := map[string]struct {
		to string
		ok bool
	}{
		"same origin":          {"https://a.example/api/y", true},
		"same host, new case":  {"https://A.EXAMPLE/api/y", true},
		"other host":           {"https://b.example/api/x", false},
		"other port":           {"https://a.example:8443/api/x", false},
		"https to http":        {"http://a.example/api/x", false},
		"subdomain is another": {"https://x.a.example/api/x", false},
	}
	for name, c := range cases {
		to, _ := url.Parse(c.to)
		err := SameOriginRedirects(&http.Request{URL: to}, []*http.Request{orig})
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
		if err != nil {
			var pe *Error
			if !errors.As(err, &pe) || pe.Status != 502 {
				t.Errorf("%s: want a 502 *Error, got %v", name, err)
			}
		}
	}
	if err := SameOriginRedirects(&http.Request{URL: from}, nil); err != nil {
		t.Errorf("no previous request: %v", err)
	}
}
