package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

const secret = "s3cret"

func sign(body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func headers(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestEachHostIsCheckedItsOwnWayAndNamesItsRepoKey(t *testing.T) {
	gh := `{"action":"opened","repository":{"full_name":"Sagpic/Sagpic-gestion"}}`
	gl := `{"object_kind":"note","project":{"web_url":"https://gitlab.com/system4us/antifaz"}}`
	jr := `{"webhookEvent":"jira:issue_updated","issue":{"self":"https://acme.atlassian.net/rest/api/3/issue/10001","fields":{"project":{"key":"PROJ"}}}}`
	rm := `{"payload":{"action":"updated","url":"http://127.0.0.1:3300/redmine/issues/7","issue":{"project":{"identifier":"backend"}}}}`
	cases := []struct {
		name string
		h    http.Header
		q    url.Values
		body string
		want Hook
	}{
		{"github", headers("X-GitHub-Event", "issues", "X-Hub-Signature-256", sign(gh)), nil, gh, Hook{"github", "issues", []string{"Sagpic/Sagpic-gestion"}}},
		{"gitlab", headers("X-Gitlab-Event", "Note Hook", "X-Gitlab-Token", secret), nil, gl, Hook{"gitlab", "Note Hook", []string{"gitlab:https://gitlab.com/system4us/antifaz"}}},
		{"jira", headers("X-Atlassian-Webhook-Identifier", "1", "X-Hub-Signature", sign(jr)), nil, jr, Hook{"jira", "jira:issue_updated", []string{"jira:https://acme.atlassian.net/PROJ"}}},
		{"generic", headers(TokenHeader, secret), nil, `{"repo":"redmine:http://r.example/backend"}`, Hook{"generic", "push", []string{"redmine:http://r.example/backend"}}},
		{"redmine plugin, token in the query", http.Header{}, url.Values{"token": {secret}}, rm, Hook{"generic", "push", []string{"redmine:http://127.0.0.1:3300/redmine/backend"}}},
		{"github ping", headers("X-GitHub-Event", "ping", "X-Hub-Signature-256", sign(`{"zen":"x"}`)), nil, `{"zen":"x"}`, Hook{Provider: "github", Event: "ping"}},
	}
	for _, c := range cases {
		got, err := Parse(c.h, c.q, []byte(c.body), secret)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestADeliveryWithoutTheSecretIsRefused(t *testing.T) {
	gh := `{"repository":{"full_name":"o/r"}}`
	cases := []struct {
		name string
		h    http.Header
		body string
		want error
	}{
		{"github unsigned", headers("X-GitHub-Event", "issues"), gh, ErrUnsigned},
		{"github signed with another secret", headers("X-GitHub-Event", "issues", "X-Hub-Signature-256", "sha256=00ff"), gh, ErrBadSignature},
		{"github body changed after signing", headers("X-GitHub-Event", "issues", "X-Hub-Signature-256", sign(gh)), gh + " ", ErrBadSignature},
		{"github old sha1 signature", headers("X-GitHub-Event", "issues", "X-Hub-Signature-256", "sha1=abc"), gh, ErrBadSignature},
		{"gitlab wrong token", headers("X-Gitlab-Event", "Issue Hook", "X-Gitlab-Token", "nope"), `{}`, ErrBadSignature},
		{"gitlab no token", headers("X-Gitlab-Event", "Issue Hook"), `{}`, ErrUnsigned},
		{"generic wrong token", headers(TokenHeader, "nope"), `{"repo":"o/r"}`, ErrBadSignature},
		{"nothing known", http.Header{}, `{"repo":"o/r"}`, ErrUnknown},
	}
	for _, c := range cases {
		if _, err := Parse(c.h, nil, []byte(c.body), secret); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	// no secret configured: nothing passes, not even a delivery signed with the empty key
	mac := hmac.New(sha256.New, nil)
	mac.Write([]byte(gh))
	empty := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if _, err := Parse(headers("X-GitHub-Event", "issues", "X-Hub-Signature-256", empty), nil, []byte(gh), ""); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("an empty secret refuses: %v", err)
	}
}

func TestAGenericDeliveryMustNameARepo(t *testing.T) {
	if _, err := Parse(headers(TokenHeader, secret), nil, []byte(`{"something":"else"}`), secret); !errors.Is(err, ErrUnknown) {
		t.Fatalf("got %v", err)
	}
}
