package credentials

import (
	"strings"
	"testing"
)

func TestSplitName(t *testing.T) {
	base, origin, err := SplitName("redmine-api-key")
	if err != nil || base != "redmine-api-key" || origin != "" {
		t.Fatalf("plain = %q %q %v", base, origin, err)
	}
	base, origin, err = SplitName("jira-email@HTTPS://Jira.Example:443/some/path?x=1")
	if err != nil || base != "jira-email" || origin != "https://jira.example" {
		t.Fatalf("scoped = %q %q %v", base, origin, err)
	}
	base, origin, err = SplitName("redmine-api-key@http://[::1]:4310/")
	if err != nil || origin != "http://[::1]:4310" {
		t.Fatalf("ipv6 = %q %v", origin, err)
	}
	for _, bad := range []string{"redmine-api-key@", "redmine-api-key@not a url", "redmine-api-key@ftp://x"} {
		if _, _, err := SplitName(bad); err == nil {
			t.Fatalf("%q: no error", bad)
		}
	}
	if _, _, err := SplitName("nope"); err == nil || !strings.Contains(err.Error(), "use one of: redmine-api-key, jira-email, jira-api-token") {
		t.Fatalf("unknown = %v", err)
	}
}
