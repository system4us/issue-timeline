import { useEffect, useState } from "react"
import { saveLinks } from "./api"

/**
 * The code repos a tracker is linked to, as a dialog opened from the project selector. A linked code repo's pull requests
 * and commits join the tracker, and are tied to its issues by the key in their title, branch or messages (PROJ-12 in Jira,
 * "refs #12" or "fixes #12" in Redmine).
 */
export function LinkModal({ tracker, codeRepos, linked, label, onClose, onSaved }: { tracker: string; codeRepos: string[]; linked: string[]; label: (r: string) => string; onClose: () => void; onSaved: () => void }) {
  const [picked, setPicked] = useState<string[]>(linked)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape" && !busy) onClose() }
    document.addEventListener("keydown", esc)
    return () => document.removeEventListener("keydown", esc)
  }, [onClose, busy])

  const dirty = picked.slice().sort().join("\n") !== linked.slice().sort().join("\n")
  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      await saveLinks(tracker, picked)
      onSaved()
      onClose()
    } catch (e) {
      setError(String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="modal-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !busy) onClose() }}>
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="link-repos-title">
        <h2 id="link-repos-title">Linked code repos</h2>
        <p className="muted small">{label(tracker)}: its issues get the pull requests and commits of the repos checked here. Read-only, like the rest.</p>
        {codeRepos.length === 0 ? (
          <p className="muted small">No GitHub or GitLab repo is added yet. Add one first (Add repository…), then link it here.</p>
        ) : (
          codeRepos.map((r) => (
            <label key={r} className="modal-field">
              <input type="checkbox" checked={picked.includes(r)} onChange={(e) => setPicked(e.target.checked ? [...picked, r] : picked.filter((x) => x !== r))} /> <span>{label(r)}</span>
            </label>
          ))
        )}
        <p className="muted small">Linked: {picked.length} · a pull request or commit names an issue by its key (for example PROJ-12, or “refs #12” in Redmine).</p>
        {error && <p className="error">{error}</p>}
        <div className="modal-actions">
          <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
          <button type="button" className="primary" onClick={save} disabled={busy || !dirty || codeRepos.length === 0}>{busy ? "Saving…" : "Save"}</button>
        </div>
      </div>
    </div>
  )
}
