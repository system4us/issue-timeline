import { useEffect, useId, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react"
import { searchRepo, type SearchHit } from "./api"
import { refOf } from "./keys"

/** Searches the selected repo: by #, key, title, or content (description and comments). Picking a result opens it.
 *  The results float over the page (absolute), so opening them never moves the content below. */
export function SearchBox({ repo, onPick }: { repo: string; onPick: (kind: "issue" | "pull", key: string) => void }) {
  const [q, setQ] = useState("")
  const [hits, setHits] = useState<SearchHit[]>([])
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [active, setActive] = useState(0)
  const box = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const uid = useId()
  const listId = `${uid}-list`
  const optionId = (i: number) => `${uid}-opt-${i}`

  // a short pause after typing, so the page asks once per pause and not once per letter
  useEffect(() => {
    if (!q.trim()) { setHits([]); setError(null); setLoading(false); return }
    let live = true
    setLoading(true)
    const timer = setTimeout(() => {
      searchRepo(repo, q.trim()).then(
        (r) => { if (live) { setHits(r); setError(null); setActive(0); setLoading(false) } },
        (e) => { if (live) { setError(String(e.message ?? e)); setLoading(false) } },
      )
    }, 200)
    return () => { live = false; clearTimeout(timer) }
  }, [q, repo])

  useEffect(() => {
    if (!open) return
    const outside = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    const esc = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false) }
    document.addEventListener("mousedown", outside)
    document.addEventListener("keydown", esc)
    return () => { document.removeEventListener("mousedown", outside); document.removeEventListener("keydown", esc) }
  }, [open])

  // "/" anywhere on the page (outside a text field) focuses the search
  useEffect(() => {
    const slash = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return
      const t = e.target as HTMLElement | null
      if (t && (t.isContentEditable || t.closest("input, textarea, select"))) return
      e.preventDefault()
      input.current?.focus()
    }
    document.addEventListener("keydown", slash)
    return () => document.removeEventListener("keydown", slash)
  }, [])

  const pick = (h: SearchHit) => { setOpen(false); setQ(""); onPick(h.kind, h.key) }

  const current = hits.length ? Math.min(active, hits.length - 1) : -1
  const showPanel = open && q.trim() !== ""

  const onKeyDown = (e: ReactKeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault()
      setOpen(true)
      if (!hits.length) return
      const step = e.key === "ArrowDown" ? 1 : -1
      setActive((current + step + hits.length) % hits.length)
    } else if (e.key === "Enter") {
      if (showPanel && current >= 0) { e.preventDefault(); pick(hits[current]) }
    }
  }

  return (
    <div className="search-box" ref={box}>
      <svg className="search-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
        <circle cx="11" cy="11" r="7" />
        <path d="m20 20-3.5-3.5" />
      </svg>
      <input
        ref={input}
        type="search"
        role="combobox"
        aria-label="Search this repo"
        aria-expanded={showPanel}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={showPanel && current >= 0 ? optionId(current) : undefined}
        placeholder="Search #, title or content"
        value={q}
        onChange={(e) => { setQ(e.target.value); setOpen(true) }}
        onFocus={() => setOpen(true)}
        onKeyDown={onKeyDown}
      />
      {q === "" && <kbd className="search-kbd" aria-hidden="true">/</kbd>}
      {showPanel && (
        <div className="search-results">
          {loading && <p className="muted small search-status">Searching…</p>}
          {error && <p className="error small">{error}</p>}
          {!loading && !error && hits.length === 0 && <p className="muted small">Nothing in this repo matches “{q.trim()}”.</p>}
          <div id={listId} role="listbox" aria-label="Search results">
            {hits.map((h, i) => (
              <div
                key={`${h.kind}-${h.key}`}
                id={optionId(i)}
                role="option"
                aria-selected={i === current}
                className="search-hit"
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => pick(h)}
              >
                <span className={`pill ${h.state.toLowerCase()}`}>{h.kind === "pull" ? "PR" : "issue"}</span>{" "}
                <strong>{refOf(h.key)}</strong> {h.title}
                {h.snippet && <span className="muted small"> — {h.snippet}</span>}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
