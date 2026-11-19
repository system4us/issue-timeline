// The Analysis dashboard's layout: which widgets, in what order, how wide. Pure functions, so they are tested on their own;
// the catalog (what each widget draws) lives in widgets.tsx.

/** Widths, in columns of a 12-column grid. On a narrow screen every widget takes the whole row. */
export const SIZES = [2, 3, 4, 6, 8, 12] as const
export type Size = (typeof SIZES)[number]

export interface Placed {
  id: string // the widget of the catalog
  w: Size
  opts?: Record<string, string> // the widget's own settings (e.g. what the breakdown groups by)
}

export interface Layout { v: 1; widgets: Placed[] }

/** What the layout needs to know about a widget of the catalog. */
export interface WidgetMeta { id: string; w: Size; min: Size }

export const clampSize = (w: number, min: Size): Size => {
  const ok = SIZES.filter((s) => s >= min)
  // the nearest allowed width, the wider one on a tie
  return ok.reduce((best, s) => (Math.abs(s - w) <= Math.abs(best - w) ? s : best), ok[0]!)
}

/** One step wider (+1) or narrower (-1), never under the widget's minimum. */
export function stepSize(w: Size, dir: 1 | -1, min: Size): Size {
  const ok = SIZES.filter((s) => s >= min)
  const i = ok.indexOf(clampSize(w, min))
  return ok[Math.max(0, Math.min(ok.length - 1, i + dir))]!
}

/** Moves the widget at `from` so it ends at index `to`. */
export function move(l: Layout, from: number, to: number): Layout {
  if (from === to || from < 0 || from >= l.widgets.length) return l
  const widgets = [...l.widgets]
  const [w] = widgets.splice(from, 1)
  widgets.splice(Math.max(0, Math.min(widgets.length, to)), 0, w!)
  return { ...l, widgets }
}

export const resize = (l: Layout, i: number, w: Size): Layout =>
  ({ ...l, widgets: l.widgets.map((x, k) => (k === i ? { ...x, w } : x)) })

export const setOpts = (l: Layout, i: number, opts: Record<string, string>): Layout =>
  ({ ...l, widgets: l.widgets.map((x, k) => (k === i ? { ...x, opts: { ...x.opts, ...opts } } : x)) })

export const remove = (l: Layout, i: number): Layout => ({ ...l, widgets: l.widgets.filter((_, k) => k !== i) })

/** Adds a widget of the catalog at its default width (at the end, or at `at`). A widget is on the dashboard at most once. */
export function add(l: Layout, meta: WidgetMeta, at = l.widgets.length): Layout {
  if (l.widgets.some((w) => w.id === meta.id)) return l
  const widgets = [...l.widgets]
  widgets.splice(at, 0, { id: meta.id, w: meta.w })
  return { ...l, widgets }
}

/** A layout from a list of ids, each at its default width. */
export const fromIds = (ids: string[], catalog: WidgetMeta[]): Layout =>
  ({ v: 1, widgets: ids.flatMap((id) => { const m = catalog.find((c) => c.id === id); return m ? [{ id, w: m.w }] : [] }) })

/**
 * A layout read back from storage, made safe: unknown widgets (a later version dropped them) and duplicates go away,
 * widths snap to the allowed ones. Anything unreadable gives null, and the caller falls back to the default.
 */
export function sanitize(raw: unknown, catalog: WidgetMeta[]): Layout | null {
  if (!raw || typeof raw !== "object" || (raw as Layout).v !== 1 || !Array.isArray((raw as Layout).widgets)) return null
  const seen = new Set<string>()
  const widgets: Placed[] = []
  for (const x of (raw as Layout).widgets as unknown[]) {
    if (!x || typeof x !== "object") continue
    const { id, w, opts } = x as Partial<Placed>
    const meta = catalog.find((c) => c.id === id)
    if (!meta || seen.has(meta.id)) continue
    seen.add(meta.id)
    const clean = opts && typeof opts === "object"
      ? Object.fromEntries(Object.entries(opts).filter(([, v]) => typeof v === "string"))
      : undefined
    widgets.push({ id: meta.id, w: clampSize(typeof w === "number" ? w : meta.w, meta.min), ...(clean && Object.keys(clean).length ? { opts: clean } : {}) })
  }
  return { v: 1, widgets }
}

// Saved per repo, in this browser (so per person too). A server copy per session is the next step (it needs an operation in api/openapi.yaml).
const KEY = (repo: string) => `issue-timeline-dashboard:${repo}`

export function load(repo: string, catalog: WidgetMeta[]): Layout | null {
  try {
    const s = localStorage.getItem(KEY(repo))
    return s ? sanitize(JSON.parse(s), catalog) : null
  } catch {
    return null
  }
}

export function save(repo: string, l: Layout | null): void {
  try {
    if (l) localStorage.setItem(KEY(repo), JSON.stringify(l))
    else localStorage.removeItem(KEY(repo))
  } catch {
    // private mode or a full storage: the layout lasts until the page reloads
  }
}

/**
 * Where a dragged widget lands: the index among the others (without itself) given the widget under the pointer
 * and on which half of it the pointer is.
 */
export function dropIndex(from: number, over: number, after: boolean): number {
  const target = after ? over + 1 : over
  return target > from ? target - 1 : target
}
