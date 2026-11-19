package credentials

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recorder is a runFunc that answers from a map and records each call, so the test sees what would have run.
type recorder struct {
	calls []call
	out   string
	err   error
}

type call struct {
	name  string
	args  []string
	stdin string
}

func (r *recorder) run(_ context.Context, name string, args []string, stdin string) (string, error) {
	r.calls = append(r.calls, call{name: name, args: append([]string(nil), args...), stdin: stdin})
	return r.out, r.err
}

func TestSecretToolGetsTheValueOnStdinNeverInTheArguments(t *testing.T) {
	rec := &recorder{}
	k := secretTool{run: rec.run}
	if err := k.Set("redmine-api-key", "s3cret-value"); err != nil {
		t.Fatal(err)
	}
	c := rec.calls[0]
	if c.name != "secret-tool" || c.args[0] != "store" || c.stdin != "s3cret-value" {
		t.Fatalf("call = %+v", c)
	}
	if strings.Contains(strings.Join(c.args, " "), "s3cret-value") {
		t.Fatal("the value is an argument")
	}
	want := []string{"store", "--label", "issue-timeline redmine-api-key", "service", "issue-timeline", "name", "redmine-api-key"}
	if strings.Join(c.args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %v", c.args)
	}
}

func TestSecretToolLookupStripsTheTrailingNewlineAndAnEmptyAnswerIsNotFound(t *testing.T) {
	rec := &recorder{out: "value-1\n"}
	k := secretTool{run: rec.run}
	if v, err := k.Get("jira-email"); err != nil || v != "value-1" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	rec.out = "crlf\r\n"
	if v, _ := k.Get("jira-email"); v != "crlf" {
		t.Fatalf("CRLF: %q", v)
	}
	rec.out = "\n"
	if _, err := k.Get("jira-email"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty: err = %v", err)
	}
	rec.err = errors.New("exit status 1")
	if _, err := k.Get("jira-email"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed: err = %v", err)
	}
}

func TestSecretToolDeleteRunsClear(t *testing.T) {
	rec := &recorder{}
	if err := (secretTool{run: rec.run}).Delete("redmine-api-key@https://r.example"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rec.calls[0].args, " "); got != "clear service issue-timeline name redmine-api-key@https://r.example" {
		t.Fatalf("args = %s", got)
	}
}
