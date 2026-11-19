// Package webhook reads the deliveries of the one webhook every host shares (POST /api/webhook). Each delivery is
// checked against the one secret (WEBHOOK_SECRET) the way its host signs it, and names the repos it may be about, as
// the server's repo keys. What changed is not read from the payload: the server syncs the repo, which reads it with the
// same code as every sync, so a delivery is only a signal that something moved.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Hook is a delivery that passed its check.
type Hook struct {
	Provider string   // "github", "gitlab", "jira" or "generic"
	Event    string   // the host's name for it ("issues", "Note Hook", "jira:issue_updated"...); "ping" needs no sync
	Repos    []string // the repo keys it may be about (compared with the configured ones without case)
}

// Errors a delivery is refused with. ErrUnsigned and ErrBadSignature answer 401, ErrUnknown 400.
var (
	ErrUnsigned     = errors.New("the delivery carries no signature or token")
	ErrBadSignature = errors.New("the delivery's signature or token does not match the webhook secret")
	ErrUnknown      = errors.New("not a delivery this server reads: GitHub, GitLab, Jira, or {\"repo\": ...} with X-Issue-Timeline-Token")
)

// TokenHeader carries the secret of a generic delivery (Redmine through a plugin, a script, anything else).
const TokenHeader = "X-Issue-Timeline-Token"

// Parse checks a delivery and reads which repos it is about. query is the request's query: a generic delivery may carry
// the secret as ?token= when its sender cannot set a header (most Redmine plugins), at the cost of the secret showing in
// URLs and logs. An empty secret refuses everything.
func Parse(h http.Header, query url.Values, body []byte, secret string) (Hook, error) {
	if secret == "" {
		return Hook{}, ErrBadSignature
	}
	switch {
	case h.Get("X-GitHub-Event") != "":
		if err := checkHMAC(h.Get("X-Hub-Signature-256"), body, secret); err != nil {
			return Hook{}, err
		}
		return github(h.Get("X-GitHub-Event"), body)
	case h.Get("X-Gitlab-Event") != "":
		if err := checkToken(h.Get("X-Gitlab-Token"), secret); err != nil {
			return Hook{}, err
		}
		return gitlab(h.Get("X-Gitlab-Event"), body)
	case h.Get("X-Atlassian-Webhook-Identifier") != "" || h.Get("X-Hub-Signature") != "":
		if err := checkHMAC(h.Get("X-Hub-Signature"), body, secret); err != nil {
			return Hook{}, err
		}
		return jira(body)
	case h.Get(TokenHeader) != "" || query.Get("token") != "":
		tok := h.Get(TokenHeader)
		if tok == "" {
			tok = query.Get("token")
		}
		if err := checkToken(tok, secret); err != nil {
			return Hook{}, err
		}
		return generic(body)
	}
	return Hook{}, ErrUnknown
}

// checkHMAC compares "sha256=<hex>" with the HMAC-SHA256 of the body under the secret, in constant time.
func checkHMAC(sig string, body []byte, secret string) error {
	if sig == "" {
		return ErrUnsigned
	}
	hexSig, ok := strings.CutPrefix(sig, "sha256=")
	if !ok {
		return ErrBadSignature
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrBadSignature
	}
	return nil
}

// checkToken compares a plain token with the secret, in constant time.
func checkToken(tok, secret string) error {
	if tok == "" {
		return ErrUnsigned
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(secret)) != 1 {
		return ErrBadSignature
	}
	return nil
}

// github: the repository's full name is the repo key ("owner/name"). A ping has none to sync.
func github(event string, body []byte) (Hook, error) {
	var p struct {
		Repository *struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Hook{}, ErrUnknown
	}
	hook := Hook{Provider: "github", Event: event}
	if p.Repository != nil && p.Repository.FullName != "" {
		hook.Repos = []string{p.Repository.FullName}
	}
	return hook, nil
}

// gitlab: the project's web address is the repo key ("gitlab:https://gitlab.com/group/project").
func gitlab(event string, body []byte) (Hook, error) {
	var p struct {
		Project *struct {
			WebURL string `json:"web_url"`
		} `json:"project"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Hook{}, ErrUnknown
	}
	hook := Hook{Provider: "gitlab", Event: event}
	if p.Project != nil && p.Project.WebURL != "" {
		hook.Repos = []string{"gitlab:" + strings.TrimSuffix(p.Project.WebURL, "/")}
	}
	return hook, nil
}

// jira: the site's origin (from the issue's API address) and the project key make the repo key
// ("jira:https://acme.atlassian.net/PROJ").
func jira(body []byte) (Hook, error) {
	var p struct {
		WebhookEvent string `json:"webhookEvent"`
		Issue        *struct {
			Self   string `json:"self"`
			Fields *struct {
				Project *struct {
					Key string `json:"key"`
				} `json:"project"`
			} `json:"fields"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.WebhookEvent == "" {
		return Hook{}, ErrUnknown
	}
	hook := Hook{Provider: "jira", Event: p.WebhookEvent}
	if i := p.Issue; i != nil && i.Fields != nil && i.Fields.Project != nil && i.Fields.Project.Key != "" {
		if origin := originOf(i.Self); origin != "" {
			hook.Repos = []string{"jira:" + origin + "/" + i.Fields.Project.Key}
		}
	}
	return hook, nil
}

// generic: {"repo": "<repo key>"}, or the payload of the redmine_webhook plugin, whose issue URL and project
// identifier make a Redmine repo key ("redmine:http://redmine.example/backend").
func generic(body []byte) (Hook, error) {
	var p struct {
		Repo    string `json:"repo"`
		Payload *struct {
			URL   string `json:"url"`
			Issue *struct {
				Project *struct {
					Identifier string `json:"identifier"`
				} `json:"project"`
			} `json:"issue"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Hook{}, ErrUnknown
	}
	hook := Hook{Provider: "generic", Event: "push"}
	switch {
	case p.Repo != "":
		hook.Repos = []string{p.Repo}
	case p.Payload != nil && p.Payload.Issue != nil && p.Payload.Issue.Project != nil && p.Payload.Issue.Project.Identifier != "":
		if base := redmineBase(p.Payload.URL); base != "" {
			hook.Repos = []string{"redmine:" + base + "/" + p.Payload.Issue.Project.Identifier}
		}
	default:
		return Hook{}, ErrUnknown
	}
	return hook, nil
}

// originOf is the scheme and host of an address, "" when it has none.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + u.Host
}

// redmineBase is the address of the Redmine an issue URL is on: everything before "/issues/", so a Redmine served under
// a path ("http://host/redmine/issues/1") keeps it; the origin when the URL has no such part.
func redmineBase(raw string) string {
	origin := originOf(raw)
	if origin == "" {
		return ""
	}
	u, _ := url.Parse(raw)
	if i := strings.Index(u.Path, "/issues/"); i > 0 {
		return origin + strings.TrimSuffix(u.Path[:i], "/")
	}
	return origin
}
