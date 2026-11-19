import { useEffect, useRef, useState } from "react"
import { LinkModal } from "./LinkPanel"
import { fetchAvailableRepos, type AvailableRepo, type RepoMode } from "./api"

export type Provider = "github" | "gitlab" | "redmine" | "jira"

interface Props {
  repos: string[]
  current: string
  label: (repo: string) => string
  onPick: (repo: string) => void
  /** Adds the repository; rejects with the message to show when the host refuses it. */
  onAdd: (repo: string, provider: Provider, mode: RepoMode) => Promise<void>
  /** Removes a configured repository. */
  onRemove?: (repo: string) => Promise<void>
  /** Each repo's mode: "read" unless it was switched to "write". */
  modes?: Record<string, RepoMode>
  /** Switches the mode of a repo. */
  onMode?: (repo: string, mode: RepoMode) => Promise<void>
  /** A tracker (Jira, Redmine) can link code repos (GitHub, GitLab): its menu offers the choice. */
  linked?: string[]
  codeRepos?: string[]
  onLinksSaved?: () => void
  /** False off the server's own machine: adding and linking repos is not offered there (the server refuses it). */
  canConfigure?: boolean
}

/** The repository the page shows, with a menu of the others and "Add repository…" as its last entry (like a realm picker). */
export function RepoSwitcher({ repos, current, label, onPick, onAdd, onRemove, linked = [], codeRepos = [], onLinksSaved = () => {}, modes = {}, onMode = async () => {}, canConfigure = true }: Props) {
  const [modeOpen, setModeOpen] = useState(false)
  const [open, setOpen] = useState(false)
  const [addOpen, setAddOpen] = useState(false)
  const [removeOpen, setRemoveOpen] = useState(false)
  const [linksOpen, setLinksOpen] = useState(false)
  const isTracker = /^(jira|redmine):/.test(current)
  const box = useRef<HTMLDivElement>(null)

  // Closes on a click outside, and on Escape.
  useEffect(() => {
    if (!open) return
    const outside = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false) }
    document.addEventListener("mousedown", outside)
    document.addEventListener("keydown", esc)
    return () => { document.removeEventListener("mousedown", outside); document.removeEventListener("keydown", esc) }
  }, [open])

  return (
    <div className="repo-switch" ref={box}>
      <button className="repo-trigger" aria-haspopup="listbox" aria-expanded={open} onClick={() => setOpen(!open)} title={current ? `${label(current)} · switch repository` : "Switch repository"}>
        <span className="repo-trigger-name">{current ? label(current) : "No repository"}</span>
        {isTracker && <span className="muted small"> · {linked.length ? `${linked.length} code repo${linked.length > 1 ? "s" : ""}` : "no code repo"}</span>}
        <span aria-hidden="true">▾</span>
      </button>
      {open && (
        <div className="repo-menu" role="listbox" aria-label="Repositories">
          {repos.map((r) => (
            <button key={r} role="option" aria-selected={r === current} className={`repo-item${r === current ? " on" : ""}`} onClick={() => { setOpen(false); if (r !== current) onPick(r) }}>
              <span className="repo-check" aria-hidden="true">{r === current ? "✓" : ""}</span>
              <span className="repo-item-name">{label(r)}</span>
              {/^(jira|redmine|gitlab|github):|^[\w.-]+\/[\w.-]+$/.test(r) && <span className={`repo-mode ${modes[r] === "write" ? "write" : "read"}`}>{modes[r] === "write" ? "write" : "read"}</span>}
            </button>
          ))}
          {repos.length > 0 && <div className="repo-sep" />}
          {current && (
            <button className="repo-item" onClick={() => { setOpen(false); setModeOpen(true) }}>
              <span className="repo-check" aria-hidden="true">✎</span>
              <span>Repository mode…</span>
            </button>
          )}
          {isTracker && canConfigure && (
            <button className="repo-item" onClick={() => { setOpen(false); setLinksOpen(true) }}>
              <span className="repo-check" aria-hidden="true">⇄</span>
              <span>Linked code repos…</span>
            </button>
          )}
          {canConfigure && (
            <button className="repo-item repo-add-item" onClick={() => { setOpen(false); setAddOpen(true) }}>
              <span className="repo-check" aria-hidden="true">+</span>
              <span>Add repository…</span>
            </button>
          )}
          {canConfigure && current && onRemove && (
            <button className="repo-item error" onClick={() => { setOpen(false); setRemoveOpen(true) }}>
              <span className="repo-check" aria-hidden="true">×</span>
              <span>Remove {label(current)}…</span>
            </button>
          )}
        </div>
      )}
      {addOpen && <AddRepoModal onClose={() => setAddOpen(false)} onAdd={onAdd} />}
      {modeOpen && current && <ModeModal repo={current} label={label} mode={modes[current] ?? "read"} onClose={() => setModeOpen(false)} onSave={async (m) => { await onMode(current, m) }} />}
      {removeOpen && current && onRemove && <RemoveModal repo={current} label={label} onClose={() => setRemoveOpen(false)} onRemove={async () => { await onRemove(current) }} />}
      {linksOpen && <LinkModal tracker={current} codeRepos={codeRepos} linked={linked} label={label} onClose={() => setLinksOpen(false)} onSaved={onLinksSaved} />}
    </div>
  )
}

function repoPlaceholder(provider: Provider): string {
  switch (provider) {
    case "github":
      return "Search your repos or enter owner/repo"
    case "gitlab":
      return "Search your projects or enter group/project or URL"
    case "redmine":
      return "https://redmine.example/projects/identifier"
    case "jira":
      return "https://acme.atlassian.net/jira/software/projects/PROJ"
  }
}

/** Filters and selects from repositories available to the caller's GitHub/GitLab account, falling back to manual entry. */
export function RepoPicker({ provider, value, onChange, onPickAdd, disabled = false, inputRef }: {
  provider: Provider
  value: string
  onChange: (value: string) => void
  onPickAdd?: (repo: string) => void
  disabled?: boolean
  inputRef?: React.RefObject<HTMLInputElement>
}) {
  const supportsList = provider === "github" || provider === "gitlab"
  const [items, setItems] = useState<AvailableRepo[]>([])
  const [loading, setLoading] = useState(false)
  const [warnings, setWarnings] = useState<string[]>([])

  useEffect(() => {
    if (!supportsList) {
      setItems([])
      setWarnings([])
      setLoading(false)
      return
    }
    let gone = false
    setLoading(true)
    setWarnings([])
    fetchAvailableRepos(provider)
      .then((res) => {
        if (gone) return
        setItems(res.repos)
        setWarnings(res.warnings ?? [])
      })
      .catch((err) => {
        if (gone) return
        setItems([])
        setWarnings([String((err as Error).message ?? err)])
      })
      .finally(() => {
        if (!gone) setLoading(false)
      })
    return () => { gone = true }
  }, [provider, supportsList])

  const query = value.trim().toLowerCase()
  const filtered = items.filter((it) => !query || it.name.toLowerCase().includes(query) || it.repo.toLowerCase().includes(query))

  return (
    <div className="repo-picker">
      <input ref={inputRef} aria-label="Repository" value={value} onChange={(e) => onChange(e.target.value)} placeholder={repoPlaceholder(provider)} />
      {supportsList && (
        <div className="repo-picker-box">
          {loading ? (
            <p className="muted small">Loading your repositories…</p>
          ) : filtered.length > 0 ? (
            <div className="repo-picker-list" role="listbox" aria-label="Available repositories">
              {filtered.map((it) => (
                <button
                  key={it.repo}
                  type="button"
                  role="option"
                  aria-selected={value.trim() === it.repo || value.trim() === it.name}
                  className={`repo-picker-item${it.added ? " added" : ""}`}
                  disabled={disabled || it.added}
                  onClick={() => (onPickAdd ? onPickAdd(it.repo) : onChange(it.repo))}
                  title={it.added ? `${it.name} is already added` : `Add ${it.name}`}
                >
                  <span className="repo-picker-name">{it.name}</span>
                  <span className="repo-picker-meta">
                    {it.private && <span className="repo-picker-badge">private</span>}
                    <span className={`repo-picker-action${it.added ? " done" : ""}`}>{it.added ? "Added" : "Add"}</span>
                  </span>
                </button>
              ))}
            </div>
          ) : (
            <p className="muted small">{items.length > 0 ? "No matching repositories — press Add repository to use the name typed above." : "No repositories returned for this account yet (sign in first, or enter a repository name above)."}</p>
          )}
          {warnings.length > 0 && <p className="error small">{warnings.join(" · ")}</p>}
        </div>
      )}
    </div>
  )
}

function AddRepoModal({ onClose, onAdd }: { onClose: () => void; onAdd: Props["onAdd"] }) {
  const [provider, setProvider] = useState<Provider>("github")
  const [mode, setMode] = useState<RepoMode>("read")
  const [repo, setRepo] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => { input.current?.focus() }, [])
  useEffect(() => {
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape" && !pending) onClose() }
    document.addEventListener("keydown", esc)
    return () => document.removeEventListener("keydown", esc)
  }, [onClose, pending])

  const addValue = async (target: string) => {
    if (!target.trim()) return
    setPending(true)
    setError(null)
    try {
      await onAdd(target.trim(), provider, mode)
      onClose()
    } catch (err) {
      setError(String((err as Error).message ?? err))
    } finally {
      setPending(false)
    }
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    await addValue(repo)
  }

  const busyNow = pending
  return (
    <div className="modal-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !busyNow) onClose() }}>
      <form className="modal" role="dialog" aria-modal="true" aria-labelledby="add-repo-title" onSubmit={submit}>
        <h2 id="add-repo-title">Add a repository</h2>
        <label className="modal-field">
          <span>Mode</span>
          <select value={mode} onChange={(e) => setMode(e.target.value as RepoMode)}>
            <option value="read">Read: sync and look (the default)</option>
            <option value="write">Write: edits go to the host</option>
          </select>
        </label>
        <p className="muted small">The repository is synced into this app. Redmine issues can be edited from it; GitHub and GitLab are read-only.</p>
        <label className="modal-field">
          <span>Host</span>
          <select value={provider} onChange={(e) => { setProvider(e.target.value as Provider); setRepo("") }}>
            <option value="github">GitHub</option>
            <option value="gitlab">GitLab (glab)</option>
            <option value="redmine">Redmine (tracker)</option>
            <option value="jira">Jira Cloud (tracker)</option>
          </select>
        </label>
        <label className="modal-field">
          <span>Repository</span>
          <RepoPicker provider={provider} value={repo} onChange={setRepo} onPickAdd={(chosen) => { void addValue(chosen) }} disabled={busyNow} inputRef={input} />
        </label>
        <p className="muted small">{provider === "github" ? "Uses your GitHub sign-in, GITHUB_TOKEN, or the gh CLI login." : provider === "gitlab" ? "Uses your GitLab sign-in, GITLAB_TOKEN, or the glab CLI login." : provider === "redmine" ? "Uses REDMINE_API_KEY. Its issues come in; then link a GitHub or GitLab repo to it (Linked code repos) for its PRs, commits and mentions." : "Uses JIRA_EMAIL and JIRA_API_TOKEN. Its issues come in; then link a GitHub or GitLab repo to it (Linked code repos) for its PRs, commits and mentions."}</p>
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button type="button" onClick={onClose} disabled={busyNow}>Cancel</button>
          <button type="submit" className="primary" disabled={busyNow || !repo.trim()}>{busyNow ? "Adding…" : "Add repository"}</button>
        </div>
      </form>
    </div>
  )
}

/** Switches a repo between read and write. Going to write says what that means, and asks for it.  */
function ModeModal({ repo, label, mode, onClose, onSave }: { repo: string; label: (r: string) => string; mode: RepoMode; onClose: () => void; onSave: (m: RepoMode) => Promise<void> }) {
  const [choice, setChoice] = useState<RepoMode>(mode)
  const [sure, setSure] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const needsSure = choice === "write" && mode !== "write"
  const save = async () => {
    setBusy(true)
    setError(null)
    try { await onSave(choice); onClose() } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }
  return (
    <div className="modal-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !busy) onClose() }}>
      <form className="modal" role="dialog" aria-modal="true" aria-labelledby="mode-title" onSubmit={(e) => { e.preventDefault(); if (!busy && (!needsSure || sure)) void save() }}>
        <h2 id="mode-title">Mode of {label(repo)}</h2>
        <label className="modal-field check"><input type="radio" name="mode" checked={choice === "read"} onChange={() => setChoice("read")} /> <span><strong>Read</strong>: the repo is synced and shown. Nothing is written to it.</span></label>
        <label className="modal-field check"><input type="radio" name="mode" checked={choice === "write"} onChange={() => setChoice("write")} /> <span><strong>Write</strong>: the issues can be edited from this app, and the edits are saved on the host.</span></label>
        {needsSure && (
          <label className="modal-field check error"><input type="checkbox" checked={sure} onChange={(e) => setSure(e.target.checked)} /> <span>I understand that edits from this app change the real issues in {label(repo)}.</span></label>
        )}
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="primary" disabled={busy || choice === mode || (needsSure && !sure)}>{busy ? "Saving…" : "Save mode"}</button>
        </div>
      </form>
    </div>
  )
}

function RemoveModal({ repo, label, onClose, onRemove }: { repo: string; label: (r: string) => string; onClose: () => void; onRemove: () => Promise<void> }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const confirm = async () => {
    setBusy(true)
    setError(null)
    try { await onRemove(); onClose() } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }
  return (
    <div className="modal-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !busy) onClose() }}>
      <form className="modal" role="dialog" aria-modal="true" aria-labelledby="remove-title" onSubmit={(e) => { e.preventDefault(); if (!busy) void confirm() }}>
        <h2 id="remove-title">Remove {label(repo)}?</h2>
        <p className="muted small">Stops syncing this repository and removes it from the selector. Cached data on disk is kept untouched.</p>
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="submit" className="primary danger" disabled={busy}>{busy ? "Removing…" : "Remove repository"}</button>
        </div>
      </form>
    </div>
  )
}
