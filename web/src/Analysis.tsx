import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react"
import type { DepEdge, Issue, Item, Link, Payload, Pull, TreeEdge } from "./types"
import { linkIndex, type Filters } from "./filters"
import { DAY } from "./metrics"
import { downloadCsv } from "./csv"
import type { CiSummary } from "./CiBadge"
import { refOf } from "./keys"
import {
  add, clampSize, dropIndex, fromIds, load, move, remove, resize, save, setOpts, stepSize, type Layout,
} from "./dashboard"
import { CATALOG, DEFAULT_IDS, PRESETS, available, byId, type Ctx, type Pool, type Widget } from "./widgets"
import { BulkBar } from "./BulkBar"
import { canEditItem } from "./inline/sections"

const LIST_LIMIT = 200

/** A list picked from a widget. It keeps how it was picked, not its items, so it follows the filters while it stays open. */
interface PickSpec { title: string; from: (p: Pool) => Item[]; epic?: string }

const WINDOWS: [number, string][] = [[30, "30 d"], [90, "90 d"], [180, "6 months"], [365, "1 year"], [0, "All"]]

const defaultLayout = (hasCode: boolean): Layout =>
  fromIds(hasCode ? DEFAULT_IDS : PRESETS.find((p) => p.id === "trackers")!.ids, CATALOG)

export function Analysis({ repo = "", meta, issues, pulls, links, tree, deps, all, filters, onFilters, onSelect, ci, hasCode = true, writable = false, onChanged = () => {} }: {
  repo?: string
  /** The repo is in write mode: the list a widget opens can then edit several issues at once. */
  writable?: boolean
  onChanged?: () => void
  meta?: Payload["meta"]
  hasCode?: boolean
  ci?: CiSummary
  issues: Issue[]
  pulls: Pull[]
  links: Link[]
  tree: TreeEdge[]
  deps: DepEdge[]
  all: { issues: Issue[]; pulls: Pull[] }
  filters: Filters
  onFilters: (p: Partial<Filters>) => void
  onSelect: (i: Item) => void
}) {
  const now = useMemo(() => Date.now(), [])
  // Most of a repo's life can be quiet weeks: the series start at the chosen window, not at the first issue.
  const [windowDays, setWindowDays] = useState(90)
  const from = windowDays > 0 ? now - windowDays * DAY : 0

  // ——— the layout: per repo, saved in this browser ———
  const [layout, setLayoutState] = useState<Layout>(() => load(repo, CATALOG) ?? defaultLayout(hasCode))
  const [customized, setCustomized] = useState(() => load(repo, CATALOG) !== null)
  const loadedFor = useRef(repo)
  useEffect(() => {
    if (loadedFor.current === repo) return
    loadedFor.current = repo
    const saved = load(repo, CATALOG)
    setLayoutState(saved ?? defaultLayout(hasCode))
    setCustomized(saved !== null)
  }, [repo, hasCode])
  const setLayout = (next: Layout) => { setLayoutState(next); setCustomized(true); save(repo, next) }
  const reset = () => { setLayoutState(defaultLayout(hasCode)); setCustomized(false); save(repo, null) }

  const [editing, setEditing] = useState(false)
  const [announce, setAnnounce] = useState("")

  // ——— the detailed list a widget opens ———
  const [pickSpec, setPickSpec] = useState<PickSpec | null>(null)
  const pickRef = useRef<HTMLElement>(null)
  useEffect(() => { if (pickSpec) pickRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" }) }, [pickSpec])

  // A filter change redraws every widget: the deferred copy keeps the filter bar responsive on a big repo while the
  // widgets catch up, and the grid dims meanwhile (that is each widget's loading state).
  const input = useMemo(() => ({ issues, pulls, links, tree, deps, all, filters, ci }), [issues, pulls, links, tree, deps, all, filters, ci])
  const d = useDeferredValue(input)
  const stale = d !== input

  const idx = useMemo(() => linkIndex(d.all.issues, d.all.pulls, d.links, d.tree, d.deps), [d.all, d.links, d.tree, d.deps])
  const linked = useMemo(() => new Set(d.links.map((l) => l.issue)), [d.links])
  const pool = useMemo<Pool>(() => ({ issues: d.issues, pulls: d.pulls, idx, linked }), [d.issues, d.pulls, idx, linked])
  const picked = useMemo(() => (pickSpec ? { ...pickSpec, items: pickSpec.from(pool) } : null), [pickSpec, pool])
  // The issues ticked in the list for a bulk edit, by key. A different list starts with none.
  const [ticked, setTicked] = useState<Set<string>>(new Set())
  useEffect(() => { setTicked(new Set()) }, [pickSpec])
  const editableRows = useMemo(
    () => (picked ? picked.items.slice(0, LIST_LIMIT).filter((i): i is Issue => i.kind === "issue" && canEditItem(repo, i, writable)) : []),
    [picked, repo, writable],
  )
  const tickedIssues = editableRows.filter((i) => ticked.has(i.key))
  const toggleTick = (key: string) => setTicked((s) => { const n = new Set(s); if (!n.delete(key)) n.add(key); return n })

  const ctx = useMemo<Ctx>(() => ({
    ...d, hasCode, now, from, idx, pool, onFilters, onSelect,
    list: (title, f, epic) => setPickSpec({ title, from: f, epic }),
    pickedEpic: pickSpec?.epic,
  }), [d, hasCode, now, from, idx, pool, onFilters, onSelect, pickSpec?.epic])

  const toggleEpic = (key: string) =>
    onFilters({ epics: filters.epics.includes(key) ? filters.epics.filter((e) => e !== key) : [...filters.epics, key] })

  // ——— drag and drop, resize, keyboard ———
  const gridRef = useRef<HTMLDivElement>(null)
  const [dragging, setDragging] = useState<string | null>(null)
  const focusAfter = useRef<string | null>(null)
  useEffect(() => {
    if (!focusAfter.current) return
    gridRef.current?.querySelector<HTMLElement>(`[data-grip="${focusAfter.current}"]`)?.focus()
    focusAfter.current = null
  })
  const indexOf = (id: string) => layout.widgets.findIndex((w) => w.id === id)
  const titleOf = (id: string) => byId.get(id)?.title ?? id

  const moveTo = (id: string, to: number, say = true) => {
    const from = indexOf(id)
    const target = Math.max(0, Math.min(layout.widgets.length - 1, to))
    if (from < 0 || target === from) return
    setLayout(move(layout, from, target))
    if (say) setAnnounce(`${titleOf(id)}: position ${target + 1} of ${layout.widgets.length}`)
  }
  const resizeBy = (id: string, dir: 1 | -1) => {
    const i = indexOf(id)
    const w = byId.get(id)
    if (i < 0 || !w) return
    const next = stepSize(layout.widgets[i]!.w, dir, w.min)
    setLayout(resize(layout, i, next))
    setAnnounce(`${w.title}: ${next} of 12 columns wide`)
  }
  const removeWidget = (id: string) => {
    const i = indexOf(id)
    if (i < 0) return
    setLayout(remove(layout, i))
    setAnnounce(`${titleOf(id)} removed`)
    // keep the keyboard on the dashboard: the next widget's handle (or the previous one)
    const next = layout.widgets[i + 1] ?? layout.widgets[i - 1]
    focusAfter.current = next?.id ?? null
  }

  const onGripKey = (id: string) => (e: React.KeyboardEvent) => {
    const i = indexOf(id)
    const back = e.key === "ArrowLeft" || e.key === "ArrowUp"
    const fwd = e.key === "ArrowRight" || e.key === "ArrowDown"
    if ((back || fwd) && e.shiftKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) { e.preventDefault(); resizeBy(id, fwd ? 1 : -1) }
    else if (back || fwd) { e.preventDefault(); focusAfter.current = id; moveTo(id, i + (fwd ? 1 : -1)) }
    else if (e.key === "Home" || e.key === "End") { e.preventDefault(); focusAfter.current = id; moveTo(id, e.key === "Home" ? 0 : layout.widgets.length - 1) }
    else if (e.key === "Delete" || e.key === "Backspace") { e.preventDefault(); removeWidget(id) }
  }

  // The pointer (mouse, pen or a finger on the handle) drags a widget: it takes the place of the widget it is over,
  // before or after it by the half the pointer is on. The layout changes live, so the others make room as it moves.
  // The moves are followed on the window, not on the handle: reordering moves the handle in the DOM, and the browser
  // drops a pointer capture when its element moves.
  const layoutRef = useRef(layout)
  layoutRef.current = layout
  const onGripDown = (id: string) => (e: React.PointerEvent) => {
    if (e.button !== 0) return
    e.preventDefault()
    setDragging(id)
    let lastOver = ""
    const onMove = (ev: PointerEvent) => {
      const el = document.elementFromPoint(ev.clientX, ev.clientY)?.closest<HTMLElement>("[data-widget]")
      const over = el?.dataset.widget
      if (!el || !over || over === id) return
      const r = el.getBoundingClientRect()
      const wide = r.width > (gridRef.current?.clientWidth ?? Infinity) * 0.7
      const after = wide ? ev.clientY > r.top + r.height / 2 : ev.clientX > r.left + r.width / 2
      const key = `${over}:${after}`
      if (key === lastOver) return
      lastOver = key
      const l = layoutRef.current
      const fromI = l.widgets.findIndex((w) => w.id === id)
      const to = dropIndex(fromI, l.widgets.findIndex((w) => w.id === over), after)
      if (to !== fromI) { const next = move(l, fromI, to); layoutRef.current = next; setLayout(next) }
    }
    const onUp = () => {
      window.removeEventListener("pointermove", onMove)
      window.removeEventListener("pointerup", onUp)
      window.removeEventListener("pointercancel", onUp)
      setDragging(null)
      const l = layoutRef.current
      setAnnounce(`${titleOf(id)}: position ${l.widgets.findIndex((w) => w.id === id) + 1} of ${l.widgets.length}`)
    }
    window.addEventListener("pointermove", onMove)
    window.addEventListener("pointerup", onUp)
    window.addEventListener("pointercancel", onUp)
  }

  // The handle on the right edge sets the width, snapped to the allowed ones (in twelfths of the grid).
  const onResizeDown = (id: string) => (e: React.PointerEvent) => {
    if (e.button !== 0) return
    e.preventDefault()
    const target = e.currentTarget as HTMLElement
    target.setPointerCapture(e.pointerId)
    const i = indexOf(id)
    const w = byId.get(id)!
    const startX = e.clientX
    const startW = layout.widgets[i]!.w
    const col = (gridRef.current?.clientWidth ?? 1200) / 12
    let current = layout
    const moveH = (ev: PointerEvent) => {
      const next = clampSize(startW + (ev.clientX - startX) / col, w.min)
      if (next !== current.widgets[i]!.w) { current = resize(current, i, next); setLayout(current) }
    }
    const up = () => { target.removeEventListener("pointermove", moveH); target.removeEventListener("pointerup", up); target.removeEventListener("pointercancel", up) }
    target.addEventListener("pointermove", moveH)
    target.addEventListener("pointerup", up)
    target.addEventListener("pointercancel", up)
  }

  const onBoard = new Set(layout.widgets.map((w) => w.id))
  const addable = CATALOG.filter((w) => !onBoard.has(w.id) && available(w, ctx))
  const exportCsv = (w: Widget, opts: Record<string, string>) => { const c = w.csv!(ctx, opts); downloadCsv(c.file, c.headers, c.rows) }
  const pickedOpen = picked ? picked.items.filter((i) => i.state === "OPEN").length : 0

  return (
    // Clicks inside the analysis stop here: the app closes the detail panel on any click outside it, which used to
    // clear the item a link had just selected.
    <div className={`analysis${editing ? " editing" : ""}`} onClick={(e) => e.stopPropagation()}>
      <div className="dash-bar">
        <div className="range" role="group" aria-label="Series window">
          <span className="muted">Series</span>
          {WINDOWS.map(([days, label]) => (
            <button key={label} className={windowDays === days ? "on" : ""} aria-pressed={windowDays === days} onClick={() => setWindowDays(days)}>{label}</button>
          ))}
        </div>
        {meta && (
          <span className="muted small dash-meta">
            Full read {meta.fullSyncAt ? new Date(meta.fullSyncAt).toLocaleString() : "not yet"} · last sync {meta.lastSync ? new Date(meta.lastSync).toLocaleString() : "never"}
          </span>
        )}
        <div className="dash-actions">
          {editing && (
            <>
              <select aria-label="Add a widget" value="" disabled={addable.length === 0}
                onChange={(e) => { const w = byId.get(e.target.value); if (w) { setLayout(add(layout, w, 0)); setAnnounce(`${w.title} added at the top`); focusAfter.current = w.id } }}>
                <option value="">{addable.length ? `Add a widget (${addable.length})…` : "Every widget is on the board"}</option>
                {addable.map((w) => <option key={w.id} value={w.id}>{w.title}</option>)}
              </select>
              <select aria-label="Load a preset" value=""
                onChange={(e) => { const p = PRESETS.find((x) => x.id === e.target.value); if (p) { setLayout(fromIds(p.ids, CATALOG)); setAnnounce(`Preset ${p.label} loaded`) } }}>
                <option value="">Preset…</option>
                {PRESETS.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
              </select>
              <button onClick={reset} disabled={!customized} title="Back to the default layout for this repo">Reset</button>
            </>
          )}
          <button className={editing ? "primary" : ""} aria-pressed={editing} onClick={() => setEditing(!editing)}>{editing ? "Done" : "Customize"}</button>
        </div>
      </div>
      {editing && (
        <p className="dash-help muted small">
          Drag a widget by its ⠿ handle, or focus the handle and use the arrow keys to move it, Shift + ←/→ to make it narrower or wider,
          Delete to remove it. The right edge resizes too. The layout is saved for this repo in this browser.
        </p>
      )}
      <div className="sr-only" aria-live="polite">{announce}</div>

      {picked && (
        <section className="panel pick-panel" ref={pickRef}>
          <h3>
            {picked.title}
            <span className="muted small"> · {picked.items.length} · {pickedOpen} open</span>
            <span className="pick-actions">
              {picked.epic !== undefined && (
                <button onClick={() => toggleEpic(picked.epic!)}>{filters.epics.includes(picked.epic) ? "Remove the epic filter" : "Filter everything by this epic"}</button>
              )}
              <button onClick={() => setPickSpec(null)} aria-label="Close the list" title="Close the list">×</button>
            </span>
          </h3>
          {tickedIssues.length > 0 && (
            <BulkBar repo={repo} items={tickedIssues} onClear={() => setTicked(new Set())}
              onApplied={() => onChanged()} />
          )}
          {picked.items.length === 0 ? <p className="empty">Nothing here.</p> : (
            <div className="scroll-x">
              <table className="table breakdown">
                <thead><tr>{editableRows.length > 0 && (
                  <th><input type="checkbox" aria-label="Select all the issues in the list" checked={tickedIssues.length === editableRows.length} ref={(el) => { if (el) el.indeterminate = tickedIssues.length > 0 && tickedIssues.length < editableRows.length }}
                    onChange={(e) => setTicked(e.target.checked ? new Set(editableRows.map((i) => i.key)) : new Set())} /></th>
                )}<th>State</th><th>Type</th><th>#</th><th>Title</th><th>Labels</th>{hasCode && <th className="num">PRs</th>}</tr></thead>
                <tbody>
                  {picked.items.slice(0, LIST_LIMIT).map((it) => (
                    <tr key={`${it.kind}-${it.key}`} onClick={() => onSelect(it)}>
                      {editableRows.length > 0 && (
                        <td onClick={(e) => e.stopPropagation()}>
                          {it.kind === "issue" && canEditItem(repo, it, writable) && <input type="checkbox" aria-label={`Select ${refOf(it.key)}`} checked={ticked.has(it.key)} onChange={() => toggleTick(it.key)} />}
                        </td>
                      )}
                      <td><span className={`pill ${it.state.toLowerCase()}`}>{it.state}</span></td>
                      <td>{it.kind === "issue" ? "Issue" : "PR"}</td>
                      <td><button className="link-btn" onClick={(e) => { e.stopPropagation(); onSelect(it) }}>{refOf(it.key)}</button></td>
                      <td className="wrap">{it.title}</td>
                      <td className="wrap">{it.labels.map((l) => <span key={l.name} className="label" style={{ borderColor: `#${l.color}` }}>{l.name}</span>)}</td>
                      {hasCode && <td className="num">{it.kind === "issue" ? idx.pullsOf.get(it.key)?.length ?? 0 : ""}</td>}
                    </tr>
                  ))}
                </tbody>
              </table>
              {picked.items.length > LIST_LIMIT && <p className="muted small">Showing the first {LIST_LIMIT} of {picked.items.length}.</p>}
            </div>
          )}
        </section>
      )}

      {layout.widgets.length === 0 ? (
        <div className="dash-empty">
          <p>The dashboard is empty.</p>
          <p className="muted small">{editing ? "Add a widget or load a preset above." : <>Press <b>Customize</b> to add widgets, or <button className="link-btn" onClick={reset}>go back to the default layout</button>.</>}</p>
        </div>
      ) : (
        <div className={`dash${stale ? " stale" : ""}`} ref={gridRef} aria-busy={stale}>
          {layout.widgets.map((p, i) => {
            const w = byId.get(p.id)
            if (!w) return null
            const ok = available(w, ctx)
            if (!ok && !editing) return null
            const kpi = w.id.startsWith("kpi-")
            return (
              <section key={p.id} data-widget={p.id} style={{ gridColumn: `span ${p.w}` }} aria-label={w.title}
                className={`widget${kpi ? " widget-kpi" : ""}${dragging === p.id ? " dragging" : ""}${ok ? "" : " unavailable"}`}>
                <header className="widget-head">
                  {editing && (
                    <button className="grip" data-grip={p.id} aria-roledescription="draggable widget"
                      aria-label={`Move ${w.title}, position ${i + 1} of ${layout.widgets.length}, ${p.w} of 12 columns. Arrow keys move it, Shift with left or right resizes it, Delete removes it.`}
                      onPointerDown={onGripDown(p.id)}
                      onKeyDown={onGripKey(p.id)}>⠿</button>
                  )}
                  <h3 title={w.hint}>{w.title}{w.hint && !kpi && <span className="muted small"> · {w.hint}</span>}</h3>
                  <span className="widget-actions">
                    {w.csv && ok && !editing && <button className="csv-btn" onClick={() => exportCsv(w, p.opts ?? {})} title="Download as CSV">CSV</button>}
                    {editing && (
                      <>
                        <button onClick={() => resizeBy(p.id, -1)} disabled={p.w === stepSize(p.w, -1, w.min)} aria-label={`Narrower: ${w.title}`} title="Narrower">−</button>
                        <span className="muted small" aria-hidden>{p.w}/12</span>
                        <button onClick={() => resizeBy(p.id, 1)} disabled={p.w === 12} aria-label={`Wider: ${w.title}`} title="Wider">+</button>
                        <button onClick={() => removeWidget(p.id)} aria-label={`Remove ${w.title}`} title="Remove">×</button>
                      </>
                    )}
                  </span>
                </header>
                <div className="widget-body">
                  {ok ? <w.Body ctx={ctx} opts={p.opts ?? {}} setOpts={(o) => setLayout(setOpts(layout, i, o))} />
                    : <p className="empty">{w.needs === "ci" ? "Needs CI runs other than a PR's." : "Needs a linked code repo."} Hidden outside of editing.</p>}
                </div>
                {editing && <div className="resize-edge" onPointerDown={onResizeDown(p.id)} aria-hidden title="Drag to resize" />}
              </section>
            )
          })}
        </div>
      )}
    </div>
  )
}
