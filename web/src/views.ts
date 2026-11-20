// Saved views: the state of the page a person can name, share and come back to. The server keeps each one as a JSON object it
// does not read; this file is the page's side of it: what is captured, how a stored one is checked before it is applied
// (a view may come from another person, or from an older version of the page), and the link that opens one.
import type { Layout, WidgetMeta } from "./dashboard"
import { sanitize } from "./dashboard"
import type { TimelineGroup } from "./grouping"
import type { Axis } from "./timeScale"
import type { ColorBy } from "./Timeline"

export type Tab = "timeline" | "analisis" | "commits"

export interface ViewState {
  v: 1
  /** The filters, as the URL carries them (`state=open&label=bug`). */
  query: string
  tab: Tab
  axis: Axis
  colorBy: ColorBy
  groupBy: TimelineGroup
  allLinks: boolean
  openToNow: boolean
  showTree: boolean
  showDeps: boolean
  showIssues: boolean
  showPulls: boolean
  /** The dashboard layout when it was customised for the repo; null is the default one. */
  dashboard: Layout | null
}

const TABS: Tab[] = ["timeline", "analisis", "commits"]
const AXES: Axis[] = ["tiempo", "actividad"]
const COLORS: ColorBy[] = ["estado", "etiqueta", "autor"]
const GROUPS: TimelineGroup[] = ["ninguno", "milestone", "etiqueta", "autor", "asignado", "epica"]
const QUERY_MAX = 4000

const pick = <T extends string>(v: unknown, allowed: T[], fallback: T): T => (typeof v === "string" && (allowed as string[]).includes(v) ? (v as T) : fallback)
const flag = (v: unknown, fallback: boolean) => (typeof v === "boolean" ? v : fallback)

/**
 * A stored state made safe to apply: every field is checked against what the page accepts, and the ones that are missing or
 * wrong take their default, so an old or hand-edited view still opens. Null when it is not a view at all.
 */
export function sanitizeView(raw: unknown, catalog: WidgetMeta[]): ViewState | null {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return null
  const r = raw as Record<string, unknown>
  const query = typeof r.query === "string" ? r.query.replace(/^\?/, "").slice(0, QUERY_MAX) : ""
  return {
    v: 1,
    query,
    tab: pick(r.tab, TABS, "timeline"),
    axis: pick(r.axis, AXES, "actividad"),
    colorBy: pick(r.colorBy, COLORS, "estado"),
    groupBy: pick(r.groupBy, GROUPS, "ninguno"),
    allLinks: flag(r.allLinks, false),
    openToNow: flag(r.openToNow, false),
    showTree: flag(r.showTree, false),
    showDeps: flag(r.showDeps, false),
    showIssues: flag(r.showIssues, true),
    showPulls: flag(r.showPulls, true),
    dashboard: r.dashboard ? sanitize(r.dashboard, catalog) : null,
  }
}

/** The link that opens a view: the repo and the view's id. The page reads it once and replaces it with the filters' own URL. */
export const viewLink = (origin: string, repo: string, id: string) => `${origin}/?repo=${encodeURIComponent(repo)}&view=${encodeURIComponent(id)}`

/** The repo and view a link names, or null when it names none. */
export function readViewLink(search: string): { repo: string; view: string } | null {
  const p = new URLSearchParams(search)
  const repo = p.get("repo")
  const view = p.get("view")
  return repo && view ? { repo, view } : null
}
