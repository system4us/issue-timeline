package auth

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

func TestTheRedmineRoleComesFromTheRolesInTheProject(t *testing.T) {
	def := DefaultRoleMapping()
	cases := []struct {
		admin bool
		names []string
		m     RoleMapping
		want  provider.Role
	}{
		{true, []string{}, def, provider.RoleAdmin},
		{false, []string{"Developer"}, def, provider.RoleEditor},
		{false, []string{"Manager", "Reporter"}, def, provider.RoleEditor},
		{false, []string{"Reporter"}, def, provider.RoleViewer},
		{false, []string{"Custom role"}, def, provider.RoleViewer},
		{false, nil, def, provider.RoleNone},
		{false, []string{"Developer"}, RoleMapping{Editor: []string{"QA"}}, provider.RoleViewer},
	}
	for _, c := range cases {
		if got := RedmineRole(c.admin, c.names, c.m); got != c.want {
			t.Errorf("RedmineRole(%v, %v) = %q, want %q", c.admin, c.names, got, c.want)
		}
	}
	if !reflect.DeepEqual(def.Editor, []string{"Manager", "Developer"}) {
		t.Fatalf("defaults = %v", def.Editor)
	}
}

func TestGitHubRoleFromThePermissionsTheRepoGives(t *testing.T) {
	cases := []struct {
		name string
		p    *GitHubPermissions
		want provider.Role
	}{
		{"admin", &GitHubPermissions{Admin: true, Maintain: true, Push: true, Triage: true, Pull: true}, provider.RoleAdmin},
		{"push and triage edit", &GitHubPermissions{Push: true, Triage: true, Pull: true}, provider.RoleEditor},
		{"pull alone reads", &GitHubPermissions{Pull: true}, provider.RoleViewer},
		{"triage alone edits issues", &GitHubPermissions{Triage: true, Pull: true}, provider.RoleEditor},
		{"no pull is no access", &GitHubPermissions{}, provider.RoleNone},
		{"no permissions at all", nil, provider.RoleNone},
	}
	for _, c := range cases {
		if got := GitHubRole(c.p); got != c.want {
			t.Errorf("%s: GitHubRole = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRoleMappingFileGivesTheEditorRoleNamesAndTheDefaultsForTheRest(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		f := filepath.Join(dir, "roles.json")
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if m := LoadRoleMapping(write(`{"editor":["QA"]}`)); !reflect.DeepEqual(m.Editor, []string{"QA"}) {
		t.Fatalf("editor QA: %v", m.Editor)
	}
	if m := LoadRoleMapping(write(`{"editor":[]}`)); len(m.Editor) != 0 {
		t.Fatalf("an empty list is used as it is: %v", m.Editor)
	}
	if m := LoadRoleMapping(write(`{"editor":"QA"}`)); !reflect.DeepEqual(m.Editor, []string{"Manager", "Developer"}) {
		t.Fatalf("not a list: %v", m.Editor)
	}
	if m := LoadRoleMapping(filepath.Join(dir, "missing.json")); !reflect.DeepEqual(m.Editor, []string{"Manager", "Developer"}) {
		t.Fatalf("missing file: %v", m.Editor)
	}
	if m := LoadRoleMapping(write(`{nope`)); !reflect.DeepEqual(m.Editor, []string{"Manager", "Developer"}) {
		t.Fatalf("broken file: %v", m.Editor)
	}
}
