import { useEffect, useRef, useState } from "react"
import { fetchMe, fetchSignInOptions, oauthStart, signIn, signOut, type Me, type MeAccount, type SignInMethod } from "./api"

const NAMES: Record<SignInMethod["provider"], string> = { github: "GitHub", gitlab: "GitLab", redmine: "Redmine" }

/** The host a repo key is on, as the sign-in methods name it ("github", or the instance's address); null for Jira. */
export function hostOf(repo: string): { provider: SignInMethod["provider"]; at: string } | null {
  if (!repo) return null
  if (repo.startsWith("jira:")) return null
  if (repo.startsWith("redmine:")) return { provider: "redmine", at: repo.slice("redmine:".length) }
  if (repo.startsWith("gitlab:")) return { provider: "gitlab", at: repo.slice("gitlab:".length) }
  return repo ? { provider: "github", at: "github" } : null
}

/** The sign-in method of the active repo's host, when the server offers one (the repo's address starts with its origin). */
export function methodFor(repo: string, methods: SignInMethod[]): SignInMethod | null {
  const h = hostOf(repo)
  if (!h) return null
  return methods.find((m) => m.provider === h.provider && (m.origin === h.at || h.at.startsWith(`${m.origin}/`))) ?? null
}

/** What a pasted token needs on each host, for the hint under the field. */
const TOKEN_HINT: Record<SignInMethod["provider"], string> = {
  github: "A fine-grained personal access token for the repo, with Issues and Pull requests (read, or read and write to edit) and Metadata (read).",
  gitlab: "A personal access token with the read_api scope, or api to edit.",
  redmine: "Your API key: My account → API access key (show).",
}

/**
 * The sign-in control: who is using the app, and the dialog to sign in on the active repo's host. GitHub and GitLab
 * offer their own sign-in page when the server has an app there, and a pasted token otherwise (or as well); Redmine
 * takes an API key. A Jira repo has no sign-in, so the button is not shown for it. `prompt` opens the dialog (a 401).
 */
export function SignIn({ me, repo, onChange, prompt, onClosePrompt }: { me: Me | null; repo: string; onChange: (m: Me | null) => void; prompt: boolean; onClosePrompt: () => void }) {
  const [open, setOpen] = useState(false)
  const [methods, setMethods] = useState<SignInMethod[] | null>(null)
  const [picked, setPicked] = useState<SignInMethod | null>(null)
  const [withToken, setWithToken] = useState(false)
  const [instance, setInstance] = useState("")
  const [key, setKey] = useState("")
  // a sign-in on the host's page that went wrong comes back as ?signin_error=...: say it in the dialog
  const [error, setError] = useState<string | null>(() => new URLSearchParams(window.location.search).get("signin_error"))
  const [busy, setBusy] = useState(false)
  const keyInput = useRef<HTMLInputElement>(null)
  const showing = open || prompt || error !== null

  useEffect(() => { void fetchSignInOptions().then(setMethods, () => setMethods([])) }, [repo, me])
  useEffect(() => {
    if (!error) return
    const u = new URL(window.location.href)
    u.searchParams.delete("signin_error")
    window.history.replaceState(null, "", u.pathname + u.search)
  }, [error])

  const accounts: MeAccount[] = me
    ? (me.accounts && me.accounts.length > 0 ? me.accounts : [{ provider: me.provider ?? "redmine", instance: me.instance, user: me.user }])
    : []
  const missingMethods = methods ? methods.filter((m) => !accounts.some((a) => a.provider === m.provider && a.instance === m.origin)) : []
  const active = methods ? methodFor(repo, methods) : null
  const method = picked ?? active ?? missingMethods[0] ?? methods?.[0] ?? null
  const connected = method ? accounts.find((a) => a.provider === method.provider && a.instance === method.origin) ?? null : null
  // the token form shows at once where there is no host page to go to
  const tokenForm = method !== null && (method.provider === "redmine" || !method.oauth || withToken)
  useEffect(() => { setWithToken(false); setKey(""); setInstance(method?.provider === "redmine" ? method.origin : "") }, [method?.provider, method?.origin])
  useEffect(() => { if (showing && tokenForm) keyInput.current?.focus() }, [showing, tokenForm])

  const close = () => { setOpen(false); setPicked(null); setError(null); onClosePrompt() }
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!method) return
    setBusy(true)
    setError(null)
    try {
      const m = await signIn(method.provider, method.provider === "gitlab" ? method.origin : instance.trim(), key.trim())
      setKey("")
      onChange(m)
      close()
    } catch (err) {
      setError(String((err as Error).message ?? err))
    } finally {
      setBusy(false)
    }
  }
  const leave = async () => { await signOut(); onChange(null) }
  const leaveOne = async (prov: SignInMethod["provider"], orig: string) => {
    setBusy(true)
    try {
      await signOut(prov, orig)
      const next = await fetchMe().catch(() => null)
      onChange(next)
      if (!next) close()
    } finally {
      setBusy(false)
    }
  }
  const back = window.location.pathname + window.location.search

  return (
    <>
      {me ? (
        <span className="signed-in">
          <span className="signed-in-names" title={accounts.map((a) => `${NAMES[a.provider]} (${a.user.login})`).join(" · ")}>
            {accounts.map((a) => a.user.name || a.user.login).join(" · ")}{accounts.some((a) => a.user.admin) ? " (admin)" : ""}
          </span>
          {missingMethods.length > 0 && (
            <button className="signin-btn" onClick={() => { setPicked(missingMethods[0]); setOpen(true) }} title="Connect another host account">
              + {missingMethods.map((m) => NAMES[m.provider]).join(" / ")}
            </button>
          )}
          {missingMethods.length === 0 && methods && methods.length > 1 && (
            <button className="link-btn" onClick={() => setOpen(true)} title="Manage connected accounts">accounts</button>
          )}
          <button className="link-btn" onClick={leave}>sign out</button>
        </span>
      ) : (
        (active || (!repo && methods && methods.length > 0)) && (
          <button className="signin-btn" onClick={() => setOpen(true)} title={active ? `Sign in on ${NAMES[active.provider]}: your role there decides what you can see and edit` : "Sign in"}>Sign in</button>
        )
      )}
      {showing && (
        <div className="modal-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !busy) close() }}>
          <form className="modal signin" role="dialog" aria-modal="true" aria-labelledby="signin-title" onSubmit={submit}>
            <h2 id="signin-title">{method ? `Sign in with ${NAMES[method.provider]}` : "Sign in"}</h2>
            {methods && methods.length > 1 && (
              <div className="signin-tabs" role="tablist" aria-label="Where to sign in">
                {methods.map((m) => {
                  const isConnected = accounts.some((a) => a.provider === m.provider && a.instance === m.origin)
                  return (
                    <button key={`${m.provider}-${m.origin}`} type="button" role="tab" aria-selected={m === method} className={m === method ? "on" : ""} onClick={() => setPicked(m)}>
                      {NAMES[m.provider]}{m.origin !== "github" && <span className="muted small"> {new URL(m.origin).host}</span>}{isConnected ? " ✓" : ""}
                    </button>
                  )
                })}
              </div>
            )}
            {methods && methods.length === 0 && <p className="muted">This server has no repo whose host signs people in (Jira has no sign-in yet).</p>}
            {connected && (
              <p className="small">
                Connected as <b>@{connected.user.login}</b>.{" "}
                <button type="button" className="link-btn" disabled={busy} onClick={() => void leaveOne(method!.provider, method!.origin)}>sign out</button>
              </p>
            )}
            {method && (
              <p className="muted small">
                Your {method.provider === "redmine" ? "key" : "access"} is checked against {NAMES[method.provider]} and kept for this session. Your role there decides what you can see and edit here.
              </p>
            )}
            {method && method.provider !== "redmine" && method.oauth && (
              <a className="oauth-btn" href={oauthStart(method.provider, back)}>
                Continue with {NAMES[method.provider]}{method.origin !== "github" && method.origin !== "https://gitlab.com" ? ` (${new URL(method.origin).host})` : ""}
              </a>
            )}
            {method && method.provider !== "redmine" && method.oauth && method.token && !withToken && (
              <button type="button" className="link-btn small" onClick={() => setWithToken(true)}>Use a personal access token instead</button>
            )}
            {method && tokenForm && (
              <>
                {method.provider === "redmine" && (
                  <label className="modal-field"><span>Redmine URL</span><input value={instance} onChange={(e) => setInstance(e.target.value)} placeholder="https://redmine.example.com" /></label>
                )}
                <label className="modal-field">
                  <span>{method.provider === "redmine" ? "API key" : "Personal access token"}</span>
                  <input ref={keyInput} type="password" autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} />
                </label>
                <p className="muted small hint">{TOKEN_HINT[method.provider]}</p>
              </>
            )}
            {error && <p className="error">{error}</p>}
            <div className="modal-actions">
              <button type="button" onClick={close} disabled={busy}>Cancel</button>
              {method && tokenForm && <button type="submit" className="primary" disabled={busy || !key.trim() || (method.provider === "redmine" && !instance.trim())}>{busy ? "Checking…" : "Sign in"}</button>}
            </div>
          </form>
        </div>
      )}
    </>
  )
}
