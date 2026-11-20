import type { DepEdge, Issue, Item, Link, Pull, TreeEdge } from "./types"
import { refOf } from "./keys"

export const NONE = "(none)" // the value that stands for "no milestone" / "no assignee"

export interface Filters {
  query: string
  state: "all" | "open" | "closed"
  milestones: string[]
  labels: string[]
  labelMode: "any" | "all"
  authors: string[]
  assignees: string[]
  createdFrom: string
  createdTo: string
  linkage: "any" | "with" | "without"
  epics: string[] // keys of the parent issues: the parent itself and everything under it
  parentsOnly: boolean // only issues that have children
  blocking: "any" | "blocked" | "blockers" // blocked by something still open / open and holding others back
  overdue: boolean // only issues: open, and past their own due date
}

/** An open issue whose own due date has passed. Compared as a day (YYYY-MM-DD), in UTC like the rest of the dates. */
export const isOverdue = (item: { kind: string; state: string; dueDate?: string | null }, now: number = Date.now()) =>
  item.kind === "issue" && item.state === "OPEN" && !!item.dueDate && item.dueDate.slice(0, 10) < new Date(now).toISOString().slice(0, 10)

export const EMPTY_FILTERS: Filters = {
  query: "", state: "all", milestones: [], labels: [], labelMode: "any", authors: [], assignees: [], createdFrom: "", createdTo: "", linkage: "any", epics: [], parentsOnly: false, blocking: "any", overdue: false,
}

/** Pull requests carry no milestone, labels or assignees of their own in most repos: they take them from the issues they close or reference. */
export function linkIndex(issues: Issue[], pulls: Pull[], links: Link[], tree: TreeEdge[] = [], deps: DepEdge[] = []) {
  const issueByNumber = new Map(issues.map((i) => [i.key, i]))
  const pullByNumber = new Map(pulls.map((p) => [p.key, p]))
  const issuesOf = new Map<string, Issue[]>() // pull key -> issues
  const pullsOf = new Map<string, Pull[]>() // issue key -> pulls
  for (const l of links) {
    const i = issueByNumber.get(l.issue)
    const p = pullByNumber.get(l.pull)
    if (!i || !p) continue
    ;(issuesOf.get(p.key) ?? issuesOf.set(p.key, []).get(p.key)!).push(i)
    ;(pullsOf.get(i.key) ?? pullsOf.set(i.key, []).get(i.key)!).push(p)
  }
  const childrenOf = new Map<string, Issue[]>() // issue key -> its children
  const parentsOf = new Map<string, Issue[]>() // issue key -> its parents (an issue can sit under more than one)
  for (const e of tree) {
    const parent = issueByNumber.get(e.parent)
    const child = issueByNumber.get(e.child)
    if (!parent || !child) continue
    ;(childrenOf.get(parent.key) ?? childrenOf.set(parent.key, []).get(parent.key)!).push(child)
    ;(parentsOf.get(child.key) ?? parentsOf.set(child.key, []).get(child.key)!).push(parent)
  }
  const blockersOf = new Map<string, Issue[]>() // issue key -> the issues that block it
  const blockedOf = new Map<string, Issue[]>() // issue key -> the issues it blocks
  for (const d of deps) {
    const blocker = issueByNumber.get(d.blocker)
    const blocked = issueByNumber.get(d.blocked)
    if (!blocker || !blocked) continue
    ;(blockersOf.get(blocked.key) ?? blockersOf.set(blocked.key, []).get(blocked.key)!).push(blocker)
    ;(blockedOf.get(blocker.key) ?? blockedOf.set(blocker.key, []).get(blocker.key)!).push(blocked)
  }
  return { issuesOf, pullsOf, childrenOf, parentsOf, blockersOf, blockedOf }
}

/** Every issue above this one in the tree, all the way up (cycles are cut). */
export function ancestors(n: string, idx: { parentsOf: Map<string, Issue[]> }): string[] {
  const seen = new Set<string>([n])
  const out: string[] = []
  const stack = [n]
  while (stack.length) {
    for (const p of idx.parentsOf.get(stack.pop()!) ?? []) {
      if (seen.has(p.key)) continue
      seen.add(p.key)
      out.push(p.key)
      stack.push(p.key)
    }
  }
  return out
}

/** Blocked "now": by an issue that is still open. A closed blocker no longer holds anything back. */
export const openBlockers = (n: string, idx: { blockersOf: Map<string, Issue[]> }) => (idx.blockersOf.get(n) ?? []).filter((b) => b.state === "OPEN")
export const openBlocked = (n: string, idx: { blockedOf: Map<string, Issue[]> }) => (idx.blockedOf.get(n) ?? []).filter((b) => b.state === "OPEN")
export type LinkIndex = ReturnType<typeof linkIndex>

const unique = <T,>(xs: T[]) => [...new Set(xs)]

export function milestonesOf(item: Item, idx: LinkIndex): string[] {
  const own = item.milestone ? [item.milestone.title] : []
  if (own.length || item.kind === "issue") return own
  return unique((idx.issuesOf.get(item.key) ?? []).flatMap((i) => (i.milestone ? [i.milestone.title] : [])))
}

export function labelsOf(item: Item, idx: LinkIndex): string[] {
  const own = item.labels.map((l) => l.name)
  if (own.length || item.kind === "issue") return own
  return unique((idx.issuesOf.get(item.key) ?? []).flatMap((i) => i.labels.map((l) => l.name)))
}

export function assigneesOf(item: Item, idx: LinkIndex): string[] {
  if (item.assignees.length || item.kind === "issue") return item.assignees
  return unique((idx.issuesOf.get(item.key) ?? []).flatMap((i) => i.assignees))
}

export function matchItem(item: Item, f: Filters, idx: LinkIndex): boolean {
  const open = item.state === "OPEN"
  if (f.state === "open" && !open) return false
  if (f.state === "closed" && open) return false
  if (f.authors.length && !(item.author && f.authors.includes(item.author))) return false
  if (f.createdFrom && item.createdAt.slice(0, 10) < f.createdFrom) return false
  if (f.createdTo && item.createdAt.slice(0, 10) > f.createdTo) return false

  if (f.milestones.length) {
    const ms = milestonesOf(item, idx)
    const ok = f.milestones.some((m) => (m === NONE ? ms.length === 0 : ms.includes(m)))
    if (!ok) return false
  }
  if (f.labels.length) {
    const ls = labelsOf(item, idx)
    const ok = f.labelMode === "all" ? f.labels.every((l) => ls.includes(l)) : f.labels.some((l) => ls.includes(l))
    if (!ok) return false
  }
  if (f.assignees.length) {
    const as = assigneesOf(item, idx)
    if (!f.assignees.some((a) => (a === NONE ? as.length === 0 : as.includes(a)))) return false
  }
  if (f.epics.length || f.parentsOnly) {
    // a pull request follows the issues behind it
    const issuesOf: Issue[] = item.kind === "issue" ? [item] : idx.issuesOf.get(item.key) ?? []
    if (f.epics.length && !issuesOf.some((i) => f.epics.includes(String(i.key)) || ancestors(i.key, idx).some((p) => f.epics.includes(String(p))))) return false
    if (f.parentsOnly && !issuesOf.some((i) => (idx.childrenOf.get(i.key)?.length ?? 0) > 0 || (idx.parentsOf.get(i.key)?.length ?? 0) > 0)) return false
  }
  if (f.blocking !== "any") {
    const issuesOf: Issue[] = item.kind === "issue" ? [item] : idx.issuesOf.get(item.key) ?? []
    const hit = issuesOf.some((i) =>
      f.blocking === "blocked" ? i.state === "OPEN" && openBlockers(i.key, idx).length > 0 : i.state === "OPEN" && openBlocked(i.key, idx).length > 0,
    )
    if (!hit) return false
  }
  if (f.linkage !== "any") {
    const has = (item.kind === "issue" ? idx.pullsOf.get(item.key) : idx.issuesOf.get(item.key))?.length
    if ((f.linkage === "with") !== !!has) return false
  }
  if (f.overdue && !isOverdue(item)) return false
  const q = f.query.trim().toLowerCase()
  return !q || item.title.toLowerCase().includes(q) || refOf(item.key).toLowerCase() === q
}

export function applyFilters(issues: Issue[], pulls: Pull[], links: Link[], f: Filters, tree: TreeEdge[] = [], deps: DepEdge[] = []) {
  const idx = linkIndex(issues, pulls, links, tree, deps)
  return { issues: issues.filter((i) => matchItem(i, f, idx)), pulls: pulls.filter((p) => matchItem(p, f, idx)), idx }
}

export const isEmpty = (f: Filters) => JSON.stringify(f) === JSON.stringify(EMPTY_FILTERS)

// The filters live in the URL, so a reload or a copied link keeps the view.
export function toQuery(f: Filters): string {
  const p = new URLSearchParams()
  if (f.query) p.set("q", f.query)
  if (f.state !== "all") p.set("estado", f.state)
  for (const m of f.milestones) p.append("milestone", m)
  for (const l of f.labels) p.append("etiqueta", l)
  if (f.labelMode !== "any") p.set("modo", f.labelMode)
  for (const a of f.authors) p.append("autor", a)
  for (const a of f.assignees) p.append("asignado", a)
  if (f.createdFrom) p.set("desde", f.createdFrom)
  if (f.createdTo) p.set("hasta", f.createdTo)
  if (f.linkage !== "any") p.set("vinculo", f.linkage)
  for (const e of f.epics) p.append("epica", e)
  if (f.parentsOnly) p.set("conhijos", "1")
  if (f.blocking !== "any") p.set("bloqueo", f.blocking)
  if (f.overdue) p.set("vencidos", "1")
  return p.toString()
}

export function fromQuery(search: string): Filters {
  const p = new URLSearchParams(search)
  const state = p.get("estado")
  const mode = p.get("modo")
  const link = p.get("vinculo")
  return {
    query: p.get("q") ?? "",
    state: state === "open" || state === "closed" ? state : "all",
    milestones: p.getAll("milestone"),
    labels: p.getAll("etiqueta"),
    labelMode: mode === "all" ? "all" : "any",
    authors: p.getAll("autor"),
    assignees: p.getAll("asignado"),
    createdFrom: p.get("desde") ?? "",
    createdTo: p.get("hasta") ?? "",
    linkage: link === "with" || link === "without" ? link : "any",
    epics: p.getAll("epica"),
    parentsOnly: p.get("conhijos") === "1",
    blocking: p.get("bloqueo") === "blocked" || p.get("bloqueo") === "blockers" ? (p.get("bloqueo") as "blocked" | "blockers") : "any",
    overdue: p.get("vencidos") === "1",
  }
}

export interface ActiveChip {
  key: string
  text: string
  /** What removing this chip changes: the filters without it. */
  patch: Partial<Filters>
}

const withoutValue = (xs: string[], v: string) => xs.filter((x) => x !== v)
const valueText = (v: string) => (v === NONE ? "(none)" : v)

/** One chip per active filter value, each able to remove itself. The label-mode choice has no chip of its own. */
export function activeChips(f: Filters): ActiveChip[] {
  const out: ActiveChip[] = []
  const q = f.query.trim()
  if (q) out.push({ key: "query", text: `search: "${q}"`, patch: { query: "" } })
  for (const m of f.milestones) out.push({ key: `milestone-${m}`, text: `milestone: ${valueText(m)}`, patch: { milestones: withoutValue(f.milestones, m) } })
  for (const l of f.labels) out.push({ key: `label-${l}`, text: `label: ${l}`, patch: { labels: withoutValue(f.labels, l) } })
  for (const e of f.epics) out.push({ key: `epic-${e}`, text: `epic: #${e}`, patch: { epics: withoutValue(f.epics, e) } })
  for (const a of f.authors) out.push({ key: `author-${a}`, text: `author: ${a}`, patch: { authors: withoutValue(f.authors, a) } })
  for (const a of f.assignees) out.push({ key: `assignee-${a}`, text: `assignee: ${valueText(a)}`, patch: { assignees: withoutValue(f.assignees, a) } })
  if (f.state !== "all") out.push({ key: "state", text: f.state === "open" ? "open only" : "closed / merged only", patch: { state: "all" } })
  if (f.linkage !== "any") out.push({ key: "linkage", text: f.linkage === "with" ? "with a link" : "no link", patch: { linkage: "any" } })
  if (f.parentsOnly) out.push({ key: "parents", text: "with children / as tree", patch: { parentsOnly: false } })
  if (f.blocking !== "any") out.push({ key: "blocking", text: f.blocking === "blocked" ? "blocked now" : "open blockers", patch: { blocking: "any" } })
  if (f.overdue) out.push({ key: "overdue", text: "overdue only", patch: { overdue: false } })
  if (f.createdFrom) out.push({ key: "from", text: `created from ${f.createdFrom}`, patch: { createdFrom: "" } })
  if (f.createdTo) out.push({ key: "to", text: `created until ${f.createdTo}`, patch: { createdTo: "" } })
  return out
}
