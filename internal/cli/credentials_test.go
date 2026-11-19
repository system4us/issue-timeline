package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/credentials"
)

// vault is a keyring that keeps values in a map, so these tests never reach the real one.
type vault struct {
	mu sync.Mutex
	m  map[string]string
}

func (v *vault) Get(name string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.m[name]; ok {
		return s, nil
	}
	return "", credentials.ErrNotFound
}

func (v *vault) Set(name, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[name] = value
	return nil
}

func (v *vault) Delete(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.m[name]; !ok {
		return credentials.ErrNotFound
	}
	delete(v.m, name)
	return nil
}

// pipe returns a read end holding text, as a piped stdin would.
func pipe(t *testing.T, text string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if _, err := w.WriteString(text); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return r
}

// noKeyring makes every secret go to the file.
type noKeyring struct{}

func (noKeyring) Get(string) (string, error) { return "", credentials.ErrNotFound }
func (noKeyring) Set(string, string) error   { return errors.New("no keyring here") }
func (noKeyring) Delete(string) error        { return credentials.ErrNotFound }

func TestTheCredentialsCommandSetsTheFirstPipedLineAndNeverPrintsIt(t *testing.T) {
	st := &credentials.Store{Dir: t.TempDir(), Env: func(string) string { return "" }, Keyring: &vault{m: map[string]string{}}}
	var out, errOut bytes.Buffer
	code := CredentialsCommand([]string{"set", "redmine-api-key", "https://r.example"}, st,
		pipe(t, "  k-123  \nsecond line\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "saved redmine-api-key@https://r.example in the OS keyring") {
		t.Fatalf("out = %q", out.String())
	}
	if strings.Contains(out.String(), "k-123") {
		t.Fatal("the value is printed")
	}
	v, _, err := st.SecretFor("redmine-api-key", "https://r.example")
	if err != nil || v != "k-123" {
		t.Fatalf("saved value = %q, %v", v, err)
	}
}

func TestTheCredentialsCommandSaysTheFileWhenThereIsNoKeyring(t *testing.T) {
	st := &credentials.Store{Dir: t.TempDir(), Env: func(string) string { return "" }, Keyring: noKeyring{}}
	var out, errOut bytes.Buffer
	if code := CredentialsCommand([]string{"set", "jira-email"}, st, pipe(t, "ana@example.com\n"), &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "saved jira-email in the owner-only file in the data folder") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestTheCredentialsCommandListsAndClears(t *testing.T) {
	st := &credentials.Store{Dir: t.TempDir(), Env: func(string) string { return "" }, Keyring: &vault{m: map[string]string{}}}
	if _, err := st.Set("redmine-api-key@https://r.example", "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Set("jira-email", "a@b.c"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := CredentialsCommand([]string{"list"}, st, nil, &out, &errOut); code != 0 {
		t.Fatalf("list code %d: %s", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{
		"redmine-api-key: not set",
		"jira-email: keyring",
		"jira-api-token: not set",
		"redmine-api-key@https://r.example: keyring",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("list =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	out.Reset()
	if code := CredentialsCommand([]string{"clear", "redmine-api-key", "https://r.example"}, st, nil, &out, &errOut); code != 0 {
		t.Fatalf("clear code %d", code)
	}
	if out.String() != "cleared redmine-api-key@https://r.example\n" {
		t.Fatalf("clear out = %q", out.String())
	}
}

func TestTheCredentialsCommandShowsItsUsageAndFailsOnAWrongOne(t *testing.T) {
	st := &credentials.Store{Dir: t.TempDir(), Env: func(string) string { return "" }, Keyring: &vault{m: map[string]string{}}}
	var out, errOut bytes.Buffer
	if code := CredentialsCommand(nil, st, nil, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "usage:") {
		t.Fatalf("no command: %d, %q", code, out.String())
	}
	out.Reset()
	if code := CredentialsCommand([]string{"bogus"}, st, nil, &out, &errOut); code != 1 || !strings.Contains(out.String(), "usage:") {
		t.Fatalf("bogus verb: %d", code)
	}
	errOut.Reset()
	if code := CredentialsCommand([]string{"set", "github"}, st, pipe(t, "x\n"), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "unknown secret") {
		t.Fatalf("unknown name: %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := CredentialsCommand([]string{"set", "jira-email", "https://r.example"}, st, pipe(t, "\n"), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "the value is empty") {
		t.Fatalf("empty value: %d, %q", code, errOut.String())
	}
}

func TestTheReadHiddenPipeTakesTheFirstLineTrimmed(t *testing.T) {
	v, err := readHidden("x: ", pipe(t, "  first \r\nsecond\n"), &bytes.Buffer{})
	if err != nil || v != "first" {
		t.Fatalf("readHidden = %q, %v", v, err)
	}
	if v, err := readHidden("x: ", pipe(t, ""), &bytes.Buffer{}); err != nil || v != "" {
		t.Fatalf("empty pipe = %q, %v", v, err)
	}
}

func TestDropLastRuneRemovesAWholeCharacter(t *testing.T) {
	if got := string(dropLastRune([]byte("a\u00e9"))); got != "a" {
		t.Fatalf("dropLastRune = %q", got)
	}
	if got := dropLastRune(nil); len(got) != 0 {
		t.Fatalf("empty = %q", got)
	}
}
