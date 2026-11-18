package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/model"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// viewerHost is a GitHub-like host that signs a person in as a viewer of the repo. It counts the commit reads, and its
// visibility can hide the code.
type viewerHost struct {
	*fakeCode
	hideCode bool
	commits  atomic.Int32
}

func (h *viewerHost) WhoAmI(context.Context, string) (provider.User, error) {
	return provider.User{ID: 1, Login: "ann"}, nil
}

func (h *viewerHost) RoleOf(context.Context, provider.Cred, provider.User) (provider.Role, error) {
	return provider.RoleViewer, nil
}

func (h *viewerHost) VisibilityOf(context.Context, provider.Cred, provider.User, provider.Role, []*model.Record) (provider.Visibility, error) {
	return provider.Visibility{HideCode: h.hideCode}, nil
}

func (h *viewerHost) CommitFiles(ctx context.Context, sha string) (*model.CommitInfo, []model.HostFile, error) {
	h.commits.Add(1)
	return h.fakeCode.CommitFiles(ctx, sha)
}

// viewerServer is the server over an app whose one repo, owner/name, is signed into as a viewer. The returned host is the
// repo's host, and the id is a session of that person.
func viewerServer(t *testing.T, hideCode bool) (http.Handler, *viewerHost, string) {
	t.Helper()
	dir := t.TempDir()
	secrets := &credentials.Store{Dir: dir, Env: func(string) string { return "" }, Keyring: memKeyring{}}
	host := &viewerHost{hideCode: hideCode, fakeCode: &fakeCode{&fakeHost{key: "owner/name", prov: "github", origin: "github"}}}
	a, err := app.New(app.Config{
		StoreDir:     dir,
		PollInterval: time.Hour,
		Env:          func(string) string { return "" },
		Secrets:      secrets,
		Log:          t.Logf,
		Hosts: func(repo string) (provider.Host, error) {
			if repo != "owner/name" {
				return fakeHosts(repo)
			}
			return host, nil
		},
		OwnToken: func(context.Context, string) (string, error) { return "local-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	if _, _, err := a.AddRepo(context.Background(), "github", "owner/name", "write"); err != nil {
		t.Fatal(err)
	}
	o := opts(nil)
	id := o.Sessions.Create("github", "github", "tok", provider.User{ID: 1, Login: "ann"})
	return New(a, o), host, id
}

// unreadBody fails the test when it is read: a request body that must not be read.
type unreadBody struct {
	t    *testing.T
	read *atomic.Bool
}

func (b unreadBody) Read([]byte) (int, error) {
	b.read.Store(true)
	return 0, http.ErrBodyReadAfterClose
}

// A viewer's PATCH is refused before its body is read, so an oversized body costs nothing.
func TestAViewersPatchIsRefusedWithoutReadingTheBody(t *testing.T) {
	h, _, id := viewerServer(t, false)
	var read atomic.Bool
	req := httptest.NewRequest(http.MethodPatch, "/api/issues/1?repo=owner/name", unreadBody{t: t, read: &read})
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("x-issue-timeline", "1")
	req.Header.Set("Origin", "http://127.0.0.1:4310")
	req.Header.Set("Cookie", auth.SessionCookie+"="+id)
	rec := serve(t, h, req)
	assertJSONError(t, rec, http.StatusForbidden)
	if read.Load() {
		t.Fatal("the body of a viewer's PATCH was read")
	}
}

// Someone who may not see code gets 404 for a commit, and the host is not asked for it (so nothing is cached).
func TestACommitIsNotReadWhenTheCodeIsHidden(t *testing.T) {
	h, host, id := viewerServer(t, true)
	sha := strings.Repeat("a", 40)
	req := httptest.NewRequest(http.MethodGet, "/api/commit?repo=owner/name&sha="+sha, nil)
	req.Host = "127.0.0.1:4310"
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Cookie", auth.SessionCookie+"="+id)
	assertJSONError(t, serve(t, h, req), http.StatusNotFound)
	if n := host.commits.Load(); n != 0 {
		t.Fatalf("the host was asked for the commit %d time(s)", n)
	}
}
