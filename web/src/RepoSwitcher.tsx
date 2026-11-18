import { useEffect, useRef, useState } from "react"

export type Provider = "github" | "gitlab"

interface Props {
  repos: string[]
  current: string
  label: (repo: string) => string
  onPick: (repo: string) => void
  /** Adds the repository; rejects with the message to show when the host refuses it. */
  onAdd: (repo: string, provider: Provider) => Promise<void>
}

/** The repository the page shows, with a menu of the others and "Add repository…" as its last entry (like a realm picker). */
export function RepoSwitcher({ repos, current, label, onPick, onAdd }: Props) {
  const [open, setOpen] = useState(false)
  const [addOpen, setAddOpen] = useState(false)
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
      <button className="repo-trigger" aria-haspopup="listbox" aria-expanded={open} onClick={() => setOpen(!open)} title="Switch repository">
        <span className="repo-trigger-name">{current ? label(current) : "No repository"}</span>
        <span aria-hidden="true">▾</span>
      </button>
      {open && (
        <div className="repo-menu" role="listbox" aria-label="Repositories">
          {repos.map((r) => (
            <button key={r} role="option" aria-selected={r === current} className={`repo-item${r === current ? " on" : ""}`} onClick={() => { setOpen(false); if (r !== current) onPick(r) }}>
              <span className="repo-check" aria-hidden="true">{r === current ? "✓" : ""}</span>
              <span className="repo-item-name">{label(r)}</span>
            </button>
          ))}
          {repos.length > 0 && <div className="repo-sep" />}
          <button className="repo-item repo-add-item" onClick={() => { setOpen(false); setAddOpen(true) }}>
            <span className="repo-check" aria-hidden="true">+</span>
            <span>Add repository…</span>
          </button>
        </div>
      )}
      {addOpen && <AddRepoModal onClose={() => setAddOpen(false)} onAdd={onAdd} />}
    </div>
  )
}

function AddRepoModal({ onClose, onAdd }: { onClose: () => void; onAdd: Props["onAdd"] }) {
  const [provider, setProvider] = useState<Provider>("github")
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

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!repo.trim()) return
    setPending(true)
    setError(null)
    try {
      await onAdd(repo.trim(), provider)
      onClose()
    } catch (err) {
      setError(String((err as Error).message ?? err))
    } finally {
      setPending(false)
    }
  }

  const busyNow = pending
  return (
    <div className="modal-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !busyNow) onClose() }}>
      <form className="modal" role="dialog" aria-modal="true" aria-labelledby="add-repo-title" onSubmit={submit}>
        <h2 id="add-repo-title">Add a repository</h2>
        <p className="muted small">Read-only: the repository is synced into this app, nothing is written back to the host.</p>
        <label className="modal-field">
          <span>Host</span>
          <select value={provider} onChange={(e) => setProvider(e.target.value as Provider)}>
            <option value="github">GitHub</option>
            <option value="gitlab">GitLab (glab)</option>
          </select>
        </label>
        <label className="modal-field">
          <span>Repository</span>
          <input ref={input} value={repo} onChange={(e) => setRepo(e.target.value)} placeholder={provider === "github" ? "owner/repo" : "group/project or https://gitlab.example/group/project"} />
        </label>
        <p className="muted small">{provider === "github" ? "Uses GITHUB_TOKEN, or the gh CLI login." : "Uses the glab CLI login for the host."}</p>
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button type="button" onClick={onClose} disabled={busyNow}>Cancel</button>
          <button type="submit" className="primary" disabled={busyNow || !repo.trim()}>{busyNow ? "Adding…" : "Add repository"}</button>
        </div>
      </form>
    </div>
  )
}
