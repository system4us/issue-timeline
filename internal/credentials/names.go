// Package credentials keeps the secrets the server needs (the Redmine API key, the Jira email and token) out of the
// environment, the shell history and the store. They go to the OS keyring when it is there, and otherwise to a file with
// owner-only permissions in the data folder. A value is only ever passed on stdin, never as an argument.
//
// A secret belongs to one host. It can be set for a host on purpose ("redmine-api-key@https://redmine.example.com"), or
// set plain ("redmine-api-key"): a plain one is bound to the first host that a sync of a configured repo uses it with,
// and is never sent to another one after that. Adding a repo never binds it (it needs the per-host secret), so a repo
// added with a mistyped or hostile URL cannot make the server send a key to a host it is not for.
package credentials

import (
	"fmt"
	"net/url"
	"strings"
)

// Spec is one plain secret the server knows: its name, the environment variable that may hold it, and what it is.
type Spec struct {
	Name string
	Env  string
	Help string
}

// specs are in the order the CLI lists them.
var specs = []Spec{
	{Name: "redmine-api-key", Env: "REDMINE_API_KEY", Help: "the API key of your Redmine user (My account > API access key)"},
	{Name: "jira-email", Env: "JIRA_EMAIL", Help: "the email of the Atlassian account"},
	{Name: "jira-api-token", Env: "JIRA_API_TOKEN", Help: "an API token (id.atlassian.com > Security > API tokens)"},
}

// Specs returns the plain secrets the server knows, in a fixed order.
func Specs() []Spec {
	return append([]Spec(nil), specs...)
}

// KnownNames returns the names of the plain secrets, in the order of Specs.
func KnownNames() []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

func specOf(base string) (Spec, bool) {
	for _, s := range specs {
		if s.Name == base {
			return s, true
		}
	}
	return Spec{}, false
}

// SplitName splits "name" or "name@https://host" into the base name and the site origin ("" for a plain name). It fails
// on an unknown name, or a host that is not an http(s) URL.
func SplitName(full string) (base, origin string, err error) {
	base, rest, scoped := strings.Cut(full, "@")
	if _, ok := specOf(base); !ok {
		return "", "", fmt.Errorf("unknown secret %s; use one of: %s (optionally name@https://host)", base, strings.Join(KnownNames(), ", "))
	}
	if !scoped {
		return base, "", nil
	}
	origin, err = parseOrigin(rest)
	if err != nil {
		return "", "", err
	}
	return base, origin, nil
}

// parseOrigin is the origin of an http or https URL, as the JS URL gives it: lower case host, no default port, no path.
func parseOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not a host URL: %s", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("the host must be an http or https URL")
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if port == "" || (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		return scheme + "://" + host, nil
	}
	return scheme + "://" + host + ":" + port, nil
}

// fullName is the name a secret is kept under: "name" or "name@origin".
func fullName(base, origin string) string {
	if origin == "" {
		return base
	}
	return base + "@" + origin
}
