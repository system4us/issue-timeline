package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newStore is a store in a fresh temp dir with the given keyring and an empty environment.
func newStore(t *testing.T, kr Keyring) *Store {
	t.Helper()
	return &Store{Dir: t.TempDir(), Env: func(string) string { return "" }, Keyring: kr}
}

func mustSet(t *testing.T, s *Store, name, value string) string {
	t.Helper()
	where, err := s.Set(name, value)
	if err != nil {
		t.Fatalf("Set(%s): %v", name, err)
	}
	return where
}

func TestWithoutKeyringTheValueGoesToAFileOnlyItsOwnerCanRead(t *testing.T) {
	s := newStore(t, noKeyring{})
	if where := mustSet(t, s, "jira-api-token", "tok-1"); where != "file" {
		t.Fatalf("where = %s, want file", where)
	}
	info, err := os.Stat(filepath.Join(s.Dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}
	v, src, err := s.Get("jira-api-token")
	if err != nil || v != "tok-1" || src != "file" {
		t.Fatalf("Get = %q, %q, %v; want tok-1 file", v, src, err)
	}
}

func TestAValueIsReadFromTheEnvironmentFirstThenTheKeyringThenTheFile(t *testing.T) {
	kr := newFakeKeyring()
	s := newStore(t, noKeyring{})
	mustSet(t, s, "jira-email", "ana@example.com") // in the file
	s.Keyring = kr
	kr.put("jira-email", "keyring@example.com")

	if _, src, _ := s.Get("jira-email"); src != "keyring" {
		t.Fatalf("source = %s, want keyring", src)
	}
	s.Env = func(k string) string {
		if k == "JIRA_EMAIL" {
			return "env@example.com"
		}
		return ""
	}
	if v, src, _ := s.Get("jira-email"); src != "env" || v != "env@example.com" {
		t.Fatalf("Get = %q from %s; want the environment", v, src)
	}
	s.Env = nil
	kr.clearAll()
	if v, src, _ := s.Get("jira-email"); src != "file" || v != "ana@example.com" {
		t.Fatalf("Get = %q from %s; want the file", v, src)
	}
	if _, _, err := s.Get("redmine-api-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTheListSaysWhereEachSecretIsNeverItsValueClearingRemovesItEverywhere(t *testing.T) {
	kr := newFakeKeyring()
	s := newStore(t, kr)
	mustSet(t, s, "redmine-api-key", "k-123")
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"redmine-api-key": "keyring", "jira-email": "", "jira-api-token": ""}
	if len(list) != len(want) {
		t.Fatalf("list = %v, want %v", list, want)
	}
	for k, v := range want {
		if list[k] != v {
			t.Fatalf("list[%s] = %q, want %q", k, list[k], v)
		}
	}
	for _, v := range list {
		if strings.Contains(v, "k-123") {
			t.Fatal("the list carries a value")
		}
	}
	if err := s.Clear("redmine-api-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Keyring.Get("redmine-api-key"); !errors.Is(err, ErrNotFound) {
		t.Fatal("the keyring still has the secret after Clear")
	}
	if list, _ := s.List(); list["redmine-api-key"] != "" {
		t.Fatalf("still listed after Clear: %v", list)
	}
}

func TestAnUnknownSecretNameAndAnEmptyValueAreRefused(t *testing.T) {
	s := newStore(t, noKeyring{})
	if _, err := s.Set("github", "x"); err == nil || !strings.Contains(err.Error(), "unknown secret") {
		t.Fatalf("err = %v, want unknown secret", err)
	}
	if _, err := s.Set("jira-email", ""); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want the value is empty", err)
	}
}

func TestAPlainSecretIsBoundToTheFirstSiteItIsUsedWithAndNeverSentToAnother(t *testing.T) {
	s := newStore(t, noKeyring{})
	mustSet(t, s, "redmine-api-key", "k-a")
	v, _, err := s.SecretFor("redmine-api-key", "https://a.example/x")
	if err != nil || v != "k-a" {
		t.Fatalf("SecretFor = %q, %v", v, err)
	}
	if b, _ := s.BoundOrigins(); len(b) != 1 || b["redmine-api-key"] != "https://a.example" {
		t.Fatalf("bound = %v", b)
	}
	if v, _, err := s.SecretFor("redmine-api-key", "https://a.example"); err != nil || v != "k-a" {
		t.Fatalf("same site: %q, %v", v, err)
	}
	_, _, err = s.SecretFor("redmine-api-key", "https://evil.example")
	if err == nil || !strings.Contains(err.Error(), "belongs to https://a.example") {
		t.Fatalf("other site: err = %v, want belongs to https://a.example", err)
	}
	// the environment variable is bound the same way
	s.Env = func(k string) string {
		if k == "REDMINE_API_KEY" {
			return "env"
		}
		return ""
	}
	if _, _, err := s.SecretFor("redmine-api-key", "https://evil.example"); err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Fatalf("env value to another site: err = %v", err)
	}
}

func TestASecretSetForASiteIsUsedThereAndANewPlainValueIsBoundAgain(t *testing.T) {
	s := newStore(t, noKeyring{})
	mustSet(t, s, "redmine-api-key", "k-a")
	if _, _, err := s.SecretFor("redmine-api-key", "https://a.example"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, s, "redmine-api-key@https://b.example/ignored/path", "k-b")
	if v, _, err := s.SecretFor("redmine-api-key", "https://b.example"); err != nil || v != "k-b" {
		t.Fatalf("site b: %q, %v", v, err)
	}
	list, _ := s.List()
	keys := []string{}
	for k := range list {
		keys = append(keys, k)
	}
	if len(keys) != 4 || list["redmine-api-key@https://b.example"] != "file" {
		t.Fatalf("list = %v", list)
	}
	mustSet(t, s, "redmine-api-key", "k-new")
	if b, _ := s.BoundOrigins(); len(b) != 0 {
		t.Fatalf("a new plain value is bound already: %v", b)
	}
	if _, _, err := s.SecretFor("jira-email", "https://a.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing secret: err = %v, want ErrNotFound", err)
	}
	if err := s.Clear("redmine-api-key@https://b.example"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); list["redmine-api-key@https://b.example"] != "" {
		t.Fatalf("still listed after Clear: %v", list)
	}
	if _, _, err := SplitName("redmine-api-key@ftp://x"); err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("ftp: err = %v", err)
	}
	if _, _, err := SplitName("nope@https://x"); err == nil || !strings.Contains(err.Error(), "unknown secret") {
		t.Fatalf("unknown: err = %v", err)
	}
}

func TestFilesWrittenInTheJSShapeAreRead(t *testing.T) {
	dir := t.TempDir()
	// credentials.json: values by full name, as the Node server wrote it
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"jira-api-token":"tok-js","redmine-api-key@https://r.example":"k-js"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// secret-origins.json: { bound: {base: origin}, scoped: [full names] }
	if err := os.WriteFile(filepath.Join(dir, "secret-origins.json"), []byte(`{"bound":{"redmine-api-key":"https://r.example"},"scoped":["redmine-api-key@https://r.example"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: dir, Env: func(string) string { return "" }, Keyring: noKeyring{}}

	if v, src, err := s.Get("jira-api-token"); err != nil || v != "tok-js" || src != "file" {
		t.Fatalf("Get = %q, %s, %v", v, src, err)
	}
	// the per-site secret is used for its own site (a trailing slash does not matter)
	if v, _, err := s.SecretFor("redmine-api-key", "https://r.example/"); err != nil || v != "k-js" {
		t.Fatalf("per-site SecretFor = %q, %v", v, err)
	}
	// no plain secret is in the file, so another site gets nothing
	if _, _, err := s.SecretFor("redmine-api-key", "https://other.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other site with no plain secret: err = %v, want ErrNotFound", err)
	}
	list, err := s.List()
	if err != nil || list["redmine-api-key@https://r.example"] != "file" || list["jira-api-token"] != "file" {
		t.Fatalf("List = %v, %v", list, err)
	}
	if b, _ := s.BoundOrigins(); b["redmine-api-key"] != "https://r.example" {
		t.Fatalf("bound = %v", b)
	}

	// and a write keeps the shape: the file the Node server reads again has the same keys
	mustSet(t, s, "jira-email", "ana@example.com")
	raw, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]string
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back["jira-email"] != "ana@example.com" || back["jira-api-token"] != "tok-js" {
		t.Fatalf("written file = %v", back)
	}
}

func TestAFileThatCannotBeReadIsAnErrorNotAnEmptyStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{Dir: dir, Env: func(string) string { return "" }, Keyring: noKeyring{}}
	if _, err := s.Set("jira-email", "a@b.c"); err == nil {
		t.Fatal("a write over an unreadable file must fail, or it would drop the secrets in it")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if string(raw) != "{not json" {
		t.Fatal("the unreadable file was overwritten")
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	s := newStore(t, newFakeKeyring())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		// each goroutine has its own site, so its own secret: the steps of one are not undone by another
		origin := fmt.Sprintf("https://r%d.example", i)
		full := "redmine-api-key@" + origin
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := s.Set(full, "k"); err != nil {
					t.Error(err)
					return
				}
				if v, _, err := s.SecretFor("redmine-api-key", origin); err != nil || v != "k" {
					t.Errorf("SecretFor = %q, %v", v, err)
					return
				}
				if _, err := s.List(); err != nil {
					t.Error(err)
					return
				}
				if err := s.Clear(full); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// SiteSecret reads only the secret set for the site: a plain one is neither used nor bound by it.
func TestSiteSecretNeverUsesOrBindsThePlainSecret(t *testing.T) {
	s := newStore(t, noKeyring{})
	if _, err := s.Set("redmine-api-key", "plain"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SiteSecret("redmine-api-key", "https://a.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SiteSecret used the plain secret: %v", err)
	}
	if b, _ := s.BoundOrigins(); len(b) != 0 {
		t.Fatalf("SiteSecret bound the plain secret: %v", b)
	}
	if _, err := s.Set("redmine-api-key@https://a.example", "k-a"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.SiteSecret("redmine-api-key", "https://a.example/x"); err != nil || v != "k-a" {
		t.Fatalf("SiteSecret = %q, %v", v, err)
	}
}
