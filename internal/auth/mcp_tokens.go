package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const mcpTokenPrefix = "it_mcp_"

// MCPTokenInfo is the non-secret metadata shown for an active MCP Bearer token.
type MCPTokenInfo struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Local     bool      `json:"local,omitempty"`
	Providers []string  `json:"providers"`
	CreatedAt time.Time `json:"createdAt"`
	LastUsed  time.Time `json:"lastUsed,omitempty"`
	// Expired is set when the host sign-in the token hangs from was revoked: the token no longer works and should be
	// replaced (revoke it, then create a new one from a signed-in session).
	Expired bool `json:"expired,omitempty"`
}

type mcpToken struct {
	expired   bool
	id        string
	hash      string
	label     string
	local     bool
	sess      *Session
	createdAt time.Time
	lastUsed  time.Time
}

type storedMCPToken struct {
	ID        string                `json:"id"`
	Hash      string                `json:"hash"`
	Label     string                `json:"label"`
	Local     bool                  `json:"local,omitempty"`
	Expired   bool                  `json:"expired,omitempty"`
	Accounts  []storedAccount       `json:"accounts,omitempty"`
	Roles     map[string]storedRole `json:"roles,omitempty"`
	CreatedAt time.Time             `json:"createdAt"`
	LastUsed  time.Time             `json:"lastUsed,omitempty"`
}

func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// BearerToken returns the raw Bearer token from Authorization, or "" when absent.
func BearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(h) <= len("Bearer ") || !strings.EqualFold(h[:7], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func (ss *Sessions) mcpSessionFromAccounts(id string, accts []Account, roles map[string]roleEntry, created time.Time) *Session {
	if len(accts) == 0 {
		return nil
	}
	primary := accts[len(accts)-1]
	return &Session{
		ID: "mcp:" + id, Provider: primary.Provider, Origin: primary.Origin, Token: primary.Token, User: primary.User,
		Grant: primary.Grant, Accounts: accts, roles: roles, onSave: ss.persistMCP, CreatedAt: created, LastSeen: created, now: ss.now,
	}
}

func (t *mcpToken) info() MCPTokenInfo {
	var provs []string
	if t.sess != nil {
		for _, a := range t.sess.AllAccounts() {
			name := a.Provider
			if a.User.Login != "" {
				name += ":" + a.User.Login
			}
			provs = append(provs, name)
		}
	}
	if provs == nil {
		provs = []string{}
	}
	return MCPTokenInfo{
		ID: t.id, Label: t.label, Local: t.local, Providers: provs,
		CreatedAt: t.createdAt, LastUsed: t.lastUsed, Expired: t.expired,
	}
}

func (t *mcpToken) ownedBy(from *Session, local bool) bool {
	if from == nil {
		return local && t.local
	}
	if t.local || t.sess == nil {
		return false
	}
	for _, mine := range from.AllAccounts() {
		if a, ok := t.sess.AccountFor(mine.Provider, mine.Origin); ok && a.User.Login == mine.User.Login {
			return true
		}
	}
	return false
}

// CreateMCPToken creates a persisted MCP Bearer token bound to from's accounts (or local loopback when from is nil and local is true).
// Returns the raw token once (only its SHA-256 hash is persisted) alongside its metadata.
func (ss *Sessions) CreateMCPToken(label string, from *Session, local bool) (string, MCPTokenInfo, error) {
	if from == nil && !local {
		return "", MCPTokenInfo{}, errors.New("sign in first to create an MCP token")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "MCP client"
	}
	rawBytes := make([]byte, 24)
	if _, err := rand.Read(rawBytes); err != nil {
		return "", MCPTokenInfo{}, err
	}
	raw := mcpTokenPrefix + base64.RawURLEncoding.EncodeToString(rawBytes)
	hash := sha256Hex(raw)
	id := hash[:12]

	now := ss.now()
	var sess *Session
	isLocal := true
	if from != nil {
		isLocal = false
		accts := from.AllAccounts()
		if len(accts) == 0 {
			return "", MCPTokenInfo{}, errors.New("sign in first to create an MCP token")
		}
		sess = ss.mcpSessionFromAccounts(id, accts, nil, now)
	}
	tok := &mcpToken{id: id, hash: hash, label: label, local: isLocal, sess: sess, createdAt: now}

	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.mcpByID[id] = tok
	ss.mcpHash[hash] = tok
	ss.saveMCPTokens()
	return raw, tok.info(), nil
}

// AuthenticateMCP looks up a raw Bearer token, returning its bound session (nil when Local is true).
func (ss *Sessions) AuthenticateMCP(raw string) (*Session, bool, bool) {
	if !strings.HasPrefix(raw, mcpTokenPrefix) {
		return nil, false, false
	}
	hash := sha256Hex(raw)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	tok := ss.mcpHash[hash]
	if tok == nil || tok.expired {
		return nil, false, false
	}
	now := ss.now()
	tok.lastUsed = now
	if now.Sub(ss.mcpSave) >= saveInterval {
		ss.saveMCPTokens()
	}
	return tok.sess, tok.local, true
}

// MCPTokenExpired reports whether raw is a known token whose host sign-in was revoked.
func (ss *Sessions) MCPTokenExpired(raw string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	tok := ss.mcpHash[sha256Hex(raw)]
	return tok != nil && tok.expired
}

// dropMCPAccount removes one account from MCP token id's session. When none remain the token is kept, marked expired
// (so its owner still sees and can revoke it, and it stops retrying a grant the host refuses), and nil is returned.
// Callers hold ss.mu.
func (ss *Sessions) dropMCPAccount(id, prov, origin string) *Session {
	t := ss.mcpByID[strings.TrimPrefix(id, "mcp:")]
	if t == nil || t.sess == nil {
		return nil
	}
	var kept []Account
	for _, a := range t.sess.AllAccounts() {
		if a.Provider == prov && a.Origin == origin {
			continue
		}
		kept = append(kept, a)
	}
	if len(kept) == 0 {
		t.expired = true
		ss.saveMCPTokens()
		return nil
	}
	t.sess = ss.mcpSessionFromAccounts(t.id, kept, nil, t.createdAt)
	ss.saveMCPTokens()
	return t.sess
}

// ListMCPTokens returns the active MCP tokens belonging to the caller.
func (ss *Sessions) ListMCPTokens(from *Session, local bool) []MCPTokenInfo {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	out := make([]MCPTokenInfo, 0, len(ss.mcpByID))
	for _, tok := range ss.mcpByID {
		if tok.ownedBy(from, local) {
			out = append(out, tok.info())
		}
	}
	slices.SortFunc(out, func(a, b MCPTokenInfo) int {
		if b.CreatedAt.Before(a.CreatedAt) {
			return -1
		}
		if a.CreatedAt.Before(b.CreatedAt) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// RevokeMCPToken deletes an MCP token by id when owned by the caller.
func (ss *Sessions) RevokeMCPToken(id string, from *Session, local bool) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	tok := ss.mcpByID[id]
	if tok == nil || !tok.ownedBy(from, local) {
		return false
	}
	delete(ss.mcpByID, id)
	delete(ss.mcpHash, tok.hash)
	ss.saveMCPTokens()
	return true
}

func (ss *Sessions) persistMCP() {
	if ss.mcpFile == "" {
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.saveMCPTokens()
}

func (ss *Sessions) loadMCPTokens() {
	if ss.mcpFile == "" {
		return
	}
	b, err := os.ReadFile(ss.mcpFile)
	if err != nil {
		return
	}
	var items []storedMCPToken
	if err := json.Unmarshal(b, &items); err != nil {
		return
	}
	now := ss.now()
	for _, it := range items {
		if it.ID == "" || it.Hash == "" {
			continue
		}
		var sess *Session
		if !it.Local {
			var accts []Account
			for _, ra := range it.Accounts {
				if a, ok := ss.rehydrateAccount(ra, now); ok {
					accts = append(accts, a)
				}
			}
			if len(accts) == 0 {
				continue
			}
			var roles map[string]roleEntry
			for repo, sr := range it.Roles {
				if now.Sub(sr.At) >= cacheTTL {
					continue
				}
				if roles == nil {
					roles = make(map[string]roleEntry, len(it.Roles))
				}
				roles[repo] = roleEntry{role: sr.Role, at: sr.At}
			}
			sess = ss.mcpSessionFromAccounts(it.ID, accts, roles, it.CreatedAt)
		}
		tok := &mcpToken{
			id: it.ID, hash: it.Hash, label: it.Label, local: it.Local, expired: it.Expired,
			sess: sess, createdAt: it.CreatedAt, lastUsed: it.LastUsed,
		}
		ss.mcpByID[tok.id] = tok
		ss.mcpHash[tok.hash] = tok
	}
	ss.mcpSave = now
}

func (ss *Sessions) saveMCPTokens() {
	if ss.mcpFile == "" {
		return
	}
	now := ss.now()
	items := make([]storedMCPToken, 0, len(ss.mcpByID))
	for _, tok := range ss.mcpByID {
		st := storedMCPToken{
			ID: tok.id, Hash: tok.hash, Label: tok.label, Local: tok.local, Expired: tok.expired,
			CreatedAt: tok.createdAt, LastUsed: tok.lastUsed,
		}
		if tok.sess != nil {
			for _, a := range tok.sess.AllAccounts() {
				sa := storedAccount{Provider: a.Provider, Origin: a.Origin, Token: a.Token, User: a.User}
				if a.Grant != nil {
					sa.Grant = &storedGrant{Redirect: a.Grant.Redirect, Refresh: a.Grant.Refresh, Expires: a.Grant.Expires}
				}
				st.Accounts = append(st.Accounts, sa)
			}
			st.Roles = tok.sess.snapshotRoles(now)
		}
		items = append(items, st)
	}
	b, err := json.Marshal(items)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(ss.mcpFile), 0o700); err != nil {
		return
	}
	tmp := ss.mcpFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Chmod(tmp, 0o600)
	if os.Rename(tmp, ss.mcpFile) == nil {
		ss.mcpSave = now
	}
}
