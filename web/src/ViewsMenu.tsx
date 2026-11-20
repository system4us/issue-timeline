import { useEffect, useRef, useState } from "react"
import { deleteView, fetchViews, saveView, type SavedView } from "./api"
import type { WidgetMeta } from "./dashboard"
import { sanitizeView, viewLink, type ViewState } from "./views"

/**
 * Saved views of the page: the filters, grouping and dashboard layout under a name, kept on the server so everyone who can see
 * the repo opens the same one. Opening applies it; each one has a link to share. A shared link (`?repo=..&view=..`) is applied
 * once when the page loads, through `openId`.
 */
export function ViewsMenu({ repo, catalog, capture, onApply, owner, canDeleteAll, openId, onOpened }: {
  repo: string
  catalog: WidgetMeta[]
  /** What the page shows now, to save. */
  capture: () => ViewState
  onApply: (s: ViewState) => void
  /** Who saves from here: the login, or "local" on the server's own machine. */
  owner: string
  /** The server's own machine may delete any view. */
  canDeleteAll: boolean
  /** A view named by the link the page was opened with. */
  openId?: string | null
  onOpened?: () => void
}) {
  const [views, setViews] = useState<SavedView[] | null>(null)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<{ text: string; error?: boolean } | null>(null)
  const box = useRef<HTMLSpanElement>(null)

  useEffect(() => {
    let live = true
    setViews(null)
    fetchViews(repo).then((v) => { if (live) setViews(v) }, (e) => { if (live) { setViews([]); setMessage({ text: String((e as Error).message ?? e), error: true }) } })
    return () => { live = false }
  }, [repo])

  // a view named by the link the page was opened with is applied once its list is here
  useEffect(() => {
    if (!openId || !views) return
    const found = views.find((v) => v.id === openId)
    if (found) apply(found)
    else setMessage({ text: "That shared view no longer exists.", error: true })
    onOpened?.()
  }, [openId, views])

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setOpen(false) }
    document.addEventListener("mousedown", onDown)
    return () => document.removeEventListener("mousedown", onDown)
  }, [open])

  const apply = (v: SavedView) => {
    const state = sanitizeView(v.state, catalog)
    if (!state) { setMessage({ text: `"${v.name}" is not a view this page can open.`, error: true }); return }
    onApply(state)
    setMessage({ text: `Opened "${v.name}".` })
    setOpen(false)
  }

  const mine = views?.find((v) => v.owner === owner && v.name.toLowerCase() === name.trim().toLowerCase())
  const save = async () => {
    if (!name.trim() || busy) return
    setBusy(true)
    setMessage(null)
    try {
      const saved = await saveView(repo, name.trim(), capture())
      setViews((vs) => [...(vs ?? []).filter((v) => v.id !== saved.id), saved])
      setName("")
      setMessage({ text: `Saved "${saved.name}".` })
    } catch (e) {
      setMessage({ text: String((e as Error).message ?? e), error: true })
    } finally {
      setBusy(false)
    }
  }
  const remove = async (v: SavedView) => {
    if (!window.confirm(`Delete the view "${v.name}" for everyone?`)) return
    try {
      setViews(await deleteView(repo, v.id))
      setMessage({ text: `Deleted "${v.name}".` })
    } catch (e) {
      setMessage({ text: String((e as Error).message ?? e), error: true })
    }
  }
  const copy = async (v: SavedView) => {
    const link = viewLink(window.location.origin, repo, v.id)
    try {
      await navigator.clipboard.writeText(link)
      setMessage({ text: "Link copied." })
    } catch {
      window.prompt("Copy this link", link) // no clipboard (an insecure origin): show it to copy by hand
    }
  }

  return (
    <span className="views-menu" ref={box}>
      <button onClick={() => setOpen((o) => !o)} aria-haspopup="dialog" aria-expanded={open} title="Saved views: filters, grouping and layout, shared with whoever can see this repo">
        Views{views && views.length > 0 ? ` (${views.length})` : ""} ▾
      </button>
      {open && (
        <div className="views-pop" role="dialog" aria-label="Saved views">
          {views === null ? <p className="muted small">Loading…</p> : views.length === 0 ? <p className="muted small">No saved views yet.</p> : (
            <ul className="views-list">
              {views.map((v) => (
                <li key={v.id}>
                  <button className="link-btn views-open" onClick={() => apply(v)} title={`Open "${v.name}"`}>{v.name}</button>
                  <span className="muted small">{v.owner}</span>
                  <button className="link-btn" onClick={() => void copy(v)} aria-label={`Copy the link of ${v.name}`} title="Copy a link to this view">link</button>
                  {(v.owner === owner || canDeleteAll) && <button className="link-btn" onClick={() => void remove(v)} aria-label={`Delete ${v.name}`} title="Delete this view for everyone">×</button>}
                </li>
              ))}
            </ul>
          )}
          <form className="views-save" onSubmit={(e) => { e.preventDefault(); void save() }}>
            <input aria-label="Name of the view" placeholder="Name this view…" value={name} maxLength={80} onChange={(e) => setName(e.target.value)} disabled={busy} />
            <button type="submit" disabled={!name.trim() || busy}>{mine ? "Update" : "Save"}</button>
          </form>
          {mine && <p className="muted small">Replaces your view of the same name.</p>}
        </div>
      )}
      {message && <span className={`views-msg small ${message.error ? "error" : "muted"}`} role="status">{message.text}</span>}
    </span>
  )
}
