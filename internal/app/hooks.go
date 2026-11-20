package app

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"gitlab.com/system4us/issue-timeline/internal/provider"
)

// hookPath is the route the hosts post to (see httpapi.webhookPath).
const hookPath = "/api/webhook"

// hookTimeout bounds what registering one repo's webhook may take (a list and a write on the host).
const hookTimeout = 30 * time.Second

// cgnat is the shared address range (100.64.0.0/10) Tailscale and carrier NAT use: not reachable from a host's servers.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// hookTarget is the webhook the server registers on the repos, or why it does not: REGISTER_WEBHOOK has to be true, with
// WEBHOOK_SECRET (the secret the hosts sign with) and a PUBLIC_URL the hosts can reach. An empty reason with an empty
// spec means the feature is simply off.
func (s *state) hookTarget() (spec provider.HookSpec, reason string) {
	switch strings.ToLower(strings.TrimSpace(s.env("REGISTER_WEBHOOK"))) {
	case "true", "1", "yes", "on":
	default:
		return provider.HookSpec{}, ""
	}
	secret := s.env("WEBHOOK_SECRET")
	if secret == "" {
		return provider.HookSpec{}, "REGISTER_WEBHOOK is on but WEBHOOK_SECRET is empty: the webhook would be refused"
	}
	base := strings.TrimRight(strings.TrimSpace(s.env("PUBLIC_URL")), "/")
	if base == "" {
		return provider.HookSpec{}, "REGISTER_WEBHOOK is on but PUBLIC_URL is empty: the hosts need an address to post to"
	}
	if err := reachableFromHosts(base); err != nil {
		return provider.HookSpec{}, "REGISTER_WEBHOOK is on but PUBLIC_URL " + err.Error()
	}
	return provider.HookSpec{URL: base + hookPath, Secret: secret}, ""
}

// reachableFromHosts refuses an address that GitHub or GitLab cannot post to: not a web address, localhost, or an IP
// literal on a private, loopback, link-local or shared (Tailscale) range. A name is taken as it is.
func reachableFromHosts(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("is not an http(s) address")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("points at localhost: the hosts cannot reach it")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || cgnat.Contains(ip) {
			return fmt.Errorf("is %s, a private address the hosts cannot reach (use a public name or a tunnel)", ip)
		}
	}
	return nil
}

// hookContext is the context the registration runs under: the server's, until it stops.
func (s *state) hookContext() (context.Context, context.CancelFunc) {
	s.mu.RLock()
	root := s.root
	s.mu.RUnlock()
	if root == nil {
		root = context.Background()
	}
	return context.WithTimeout(root, hookTimeout)
}

// registerHook registers the webhook on one repo when the feature is on and its host supports it. It only logs: a repo
// that refuses (no admin rights on it) keeps working through the poll, which is the safety net.
func (s *state) registerHook(repo string) {
	spec, reason := s.hookTarget()
	if spec.URL == "" {
		if reason != "" {
			s.logf(repo, "webhook not registered: %s", reason)
		}
		return
	}
	es, ok := s.get(repo)
	if !ok {
		return
	}
	reg, ok := es.Host.(provider.WebhookRegistrar)
	if !ok {
		return
	}
	ctx, cancel := s.hookContext()
	defer cancel()
	action, err := reg.EnsureWebhook(ctx, spec)
	if err != nil {
		s.logf(repo, "webhook not registered: %s", hookFailure(err))
		return
	}
	s.logf(repo, "webhook %s (%s)", action, spec.URL)
}

// registerHooks registers the webhook on every configured repo, one after another.
func (s *state) registerHooks() {
	for _, repo := range s.repos() {
		s.registerHook(repo)
	}
}

// removeHook deletes the webhook of a repo that was just removed from the server (host is that repo's).
func (s *state) removeHook(repo string, host provider.Host) {
	spec, _ := s.hookTarget()
	reg, ok := host.(provider.WebhookRegistrar)
	if spec.URL == "" || !ok {
		return
	}
	ctx, cancel := s.hookContext()
	defer cancel()
	removed, err := reg.RemoveWebhook(ctx, spec.URL)
	switch {
	case err != nil:
		s.logf(repo, "webhook not removed: %s", hookFailure(err))
	case removed:
		s.logf(repo, "webhook removed")
	}
}

// hookFailure explains a refusal in the words of what to do about it.
func hookFailure(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		switch pe.Status {
		case 401, 403:
			return "the server's credential may not manage webhooks on it (it needs admin rights): " + pe.Message
		case 404:
			return "the server's credential cannot see the repo's webhooks (it needs admin rights): " + pe.Message
		}
	}
	return err.Error()
}
