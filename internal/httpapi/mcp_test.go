package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.com/system4us/issue-timeline/internal/app"
	"gitlab.com/system4us/issue-timeline/internal/auth"
	"gitlab.com/system4us/issue-timeline/internal/credentials"
	"gitlab.com/system4us/issue-timeline/internal/provider"
)

type mcpRT func(*http.Request) (*http.Response, error)

func (f mcpRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPMCPEndpointAuthHostAndRevocation(t *testing.T) {
	dir := t.TempDir()
	host := &fakeCode{&fakeHost{key: "owner/name", prov: "github", origin: "github"}}
	a, err := app.New(app.Config{
		StoreDir: dir, PollInterval: time.Hour, Env: func(string) string { return "" },
		Secrets:  &credentials.Store{Dir: dir, Env: func(string) string { return "" }, Keyring: memKeyring{}},
		Log:      t.Logf,
		Hosts:    func(string) (provider.Host, error) { return host, nil },
		OwnToken: func(context.Context, string) (string, error) { return "srv", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Stop)
	if _, _, err := a.AddRepo(context.Background(), "github", "owner/name", "read"); err != nil {
		t.Fatal(err)
	}

	o := opts(nil)
	sid := o.Sessions.Create("github", "github", "user-tok", provider.User{ID: 7, Login: "ana"})
	sessionCookie := &http.Cookie{Name: auth.SessionCookie, Value: sid}

	srv := httptest.NewServer(New(a, o))
	defer srv.Close()

	// 1. Remote request without Bearer token is 401, even with browser cookie (CSRF immune).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req.AddCookie(sessionCookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("remote /mcp with cookie only = %d, want 401", res.StatusCode)
	}

	// 2. Host check still protects /mcp against DNS rebinding.
	reqBadHost, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(`{}`))
	reqBadHost.Host = "evil.example:4310"
	res, err = http.DefaultClient.Do(reqBadHost)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("bad Host on /mcp = %d, want 421", res.StatusCode)
	}

	// 3. Signed-in user mints a Bearer token via POST /api/mcp-tokens.
	mintReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/mcp-tokens", strings.NewReader(`{"label":"IDE"}`))
	mintReq.Header.Set("X-Forwarded-For", "203.0.113.10")
	mintReq.Header.Set("X-Issue-Timeline", "1")
	mintReq.Header.Set("Content-Type", "application/json")
	mintReq.AddCookie(sessionCookie)
	mintRes, err := http.DefaultClient.Do(mintReq)
	if err != nil {
		t.Fatal(err)
	}
	var minted struct {
		Token string `json:"token"`
		Info  struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	_ = json.NewDecoder(mintRes.Body).Decode(&minted)
	mintRes.Body.Close()
	if mintRes.StatusCode != http.StatusCreated || !strings.HasPrefix(minted.Token, "it_mcp_") {
		t.Fatalf("mint = %d %+v", mintRes.StatusCode, minted)
	}

	// 4. Connect MCP client over Streamable HTTP with Bearer token (remote X-Forwarded-For).
	hc := &http.Client{
		Transport: mcpRT(func(r *http.Request) (*http.Response, error) {
			r.Header.Set("Authorization", "Bearer "+minted.Token)
			r.Header.Set("X-Forwarded-For", "203.0.113.10")
			return http.DefaultTransport.RoundTrip(r)
		}),
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "ide", Version: "1.0.0"}, nil)
	cs, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		HTTPClient:           hc,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("MCP connect failed: %v", err)
	}
	out, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "list_repos", Arguments: map[string]any{}})
	_ = cs.Close()
	if err != nil || out.IsError || len(out.Content) == 0 {
		t.Fatalf("list_repos over /mcp failed: err=%v out=%+v", err, out)
	}

	// 5. Revoke token and confirm future /mcp calls are rejected with 401.
	revReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/mcp-tokens?id="+minted.Info.ID, nil)
	revReq.Header.Set("X-Forwarded-For", "203.0.113.10")
	revReq.Header.Set("X-Issue-Timeline", "1")
	revReq.AddCookie(sessionCookie)
	revRes, err := http.DefaultClient.Do(revReq)
	if err != nil {
		t.Fatal(err)
	}
	revRes.Body.Close()
	if revRes.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d", revRes.StatusCode)
	}
	if _, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{
		Endpoint:             srv.URL + "/mcp",
		HTTPClient:           hc,
		DisableStandaloneSSE: true,
	}, nil); err == nil {
		t.Fatal("revoked MCP token was still accepted")
	}
}
