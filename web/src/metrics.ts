import type { Issue, Link, Pull } from "./types"
import { NONE, assigneesOf, labelsOf, linkIndex, milestonesOf, openBlocked, openBlockers, type LinkIndex } from "./filters"

export const DAY = 86_400_000
const ms = (s: string | null) => (s ? Date.parse(s) : NaN)

export const days = (from: string, to: string) => (Date.parse(to) - Date.parse(from)) / DAY

export function percentile(values: number[], p: number): number | null {
  if (values.length === 0) return null
  const s = [...values].sort((a, b) => a - b)
  const idx = (s.length - 1) * p
  const lo = Math.floor(idx)
  const hi = Math.ceil(idx)
  return s[lo]! + (s[hi]! - s[lo]!) * (idx - lo)
}
export const median = (values: number[]) => percentile(values, 0.5)

/** Days from creation to close of the closed issues. */
export const issueCycleDays = (issues: Issue[]) =>
  issues.filter((i) => i.closedAt).map((i) => days(i.createdAt, i.closedAt!))

/** Days from opening to merge of the merged pull requests. */
export const pullMergeDays = (pulls: Pull[]) =>
  pulls.filter((p) => p.mergedAt).map((p) => days(p.createdAt, p.mergedAt!))

/** Days from opening to the first review of the pull requests that got one. */
export const firstReviewDays = (pulls: Pull[]) =>
  pulls.filter((p) => p.firstReviewAt).map((p) => days(p.createdAt, p.firstReviewAt!))

/** Start (UTC) of the ISO week (Monday) of a timestamp. */
export function weekStart(t: number): number {
  const d = new Date(t)
  const dow = (d.getUTCDay() + 6) % 7
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() - dow)
}

export interface WeekPoint { week: number; a: number; b: number }

/** Counts per week of two date series (e.g. created and closed), over every week between the first and `until`. */
export function perWeek(a: (string | null)[], b: (string | null)[], until: number): WeekPoint[] {
  const times = [...a, ...b].map(ms).filter((t) => !Number.isNaN(t))
  if (times.length === 0) return []
  const first = weekStart(Math.min(...times))
  const last = weekStart(until)
  const map = new Map<number, WeekPoint>()
  for (let w = first; w <= last; w += 7 * DAY) map.set(w, { week: w, a: 0, b: 0 })
  for (const t of a.map(ms)) if (!Number.isNaN(t)) map.get(weekStart(t))!.a++
  for (const t of b.map(ms)) if (!Number.isNaN(t)) map.get(weekStart(t))!.b++
  return [...map.values()]
}

export interface OpenPoint { t: number; open: number }

/** Issues open at the end of each week: created so far minus closed so far (reopened issues count as open now). */
export function openOverTime(issues: Issue[], until: number): OpenPoint[] {
  const created = issues.map((i) => ms(i.createdAt))
  if (created.length === 0) return []
  const closed = issues.filter((i) => i.closedAt).map((i) => ms(i.closedAt!))
  const out: OpenPoint[] = []
  for (let w = weekStart(Math.min(...created)); w <= until + 7 * DAY; w += 7 * DAY) {
    const end = Math.min(w + 7 * DAY, until)
    out.push({ t: end, open: created.filter((c) => c <= end).length - closed.filter((c) => c <= end).length })
  }
  return out
}

export interface Bin { from: number; to: number; count: number }

/** Histogram with the given upper edges (the last bin is open-ended). */
export function histogram(values: number[], edges: number[]): Bin[] {
  const bins: Bin[] = edges.map((to, i) => ({ from: i === 0 ? 0 : edges[i - 1]!, to, count: 0 }))
  bins.push({ from: edges[edges.length - 1]!, to: Infinity, count: 0 })
  for (const v of values) bins.find((b) => v >= b.from && v < b.to)!.count++
  return bins
}

export function oldestOpen(issues: Issue[], now: number, top = 10) {
  return issues
    .filter((i) => i.state === "OPEN")
    .map((i) => ({ issue: i, age: (now - ms(i.createdAt)) / DAY }))
    .sort((a, b) => b.age - a.age)
    .slice(0, top)
}

export function labelCounts(issues: Issue[]) {
  const counts = new Map<string, { name: string; color: string; open: number; closed: number }>()
  for (const i of issues) {
    for (const l of i.labels) {
      const c = counts.get(l.name) ?? { name: l.name, color: l.color, open: 0, closed: 0 }
      if (i.state === "OPEN") c.open++
      else c.closed++
      counts.set(l.name, c)
    }
  }
  return [...counts.values()].sort((a, b) => b.open + b.closed - (a.open + a.closed))
}

/** Cross of the two kinds: how issues that have a pull request behind them differ from the ones that do not. */
export function issuesWithAndWithoutPull(issues: Issue[], links: Link[]) {
  const linked = new Set(links.map((l) => l.issue))
  const closed = issues.filter((i) => i.closedAt)
  return {
    withPull: { count: closed.filter((i) => linked.has(i.number)).length, median: median(issueCycleDays(closed.filter((i) => linked.has(i.number)))) },
    withoutPull: { count: closed.filter((i) => !linked.has(i.number)).length, median: median(issueCycleDays(closed.filter((i) => !linked.has(i.number)))) },
    openWithoutPull: issues.filter((i) => i.state === "OPEN" && !linked.has(i.number)).length,
  }
}

/** Size of a pull request against how long it took to merge. */
export function sizeVsMerge(pulls: Pull[]) {
  return pulls
    .filter((p) => p.mergedAt)
    .map((p) => ({ number: p.number, size: p.additions + p.deletions, days: days(p.createdAt, p.mergedAt!) }))
}

/** Pull requests merged within [from, to). */
export const mergedBetween = (pulls: Pull[], from: number, to: number) =>
  pulls.filter((p) => p.mergedAt && ms(p.mergedAt) >= from && ms(p.mergedAt) < to)

export type GroupBy = "milestone" | "etiqueta" | "autor" | "asignado"

export interface BreakdownRow {
  key: string
  total: number
  open: number
  closed: number
  pct: number // share of the issues that are closed
  medianCycle: number | null
  p90Cycle: number | null
  pulls: number // distinct pull requests behind these issues
  merged: number
  medianMerge: number | null
  oldestOpen: number | null // days
  withoutPull: number // open issues with no pull request
  due: string | null
  overdue: boolean
}

const keysOf = (issue: Issue, by: GroupBy, idx: LinkIndex): string[] => {
  const none = (xs: string[]) => (xs.length ? xs : [NONE])
  switch (by) {
    case "milestone": return none(milestonesOf(issue, idx))
    case "etiqueta": return none(labelsOf(issue, idx))
    case "autor": return [issue.author ?? NONE]
    case "asignado": return none(assigneesOf(issue, idx))
  }
}

/** One row per milestone / label / author / assignee: how the issues of the group are going, and the pull requests behind them. */
export function breakdown(issues: Issue[], links: Link[], pulls: Pull[], by: GroupBy, now: number): BreakdownRow[] {
  const idx = linkIndex(issues, pulls, links)
  const groups = new Map<string, Issue[]>()
  for (const i of issues) for (const k of keysOf(i, by, idx)) (groups.get(k) ?? groups.set(k, []).get(k)!).push(i)
  const rows: BreakdownRow[] = []
  for (const [key, list] of groups) {
    const closed = list.filter((i) => i.state === "CLOSED")
    const cycle = issueCycleDays(closed)
    const pullSet = new Map<number, Pull>()
    for (const i of list) for (const p of idx.pullsOf.get(i.number) ?? []) pullSet.set(p.number, p)
    const open = list.filter((i) => i.state === "OPEN")
    const due = by === "milestone" ? list.find((i) => i.milestone?.dueOn)?.milestone?.dueOn ?? null : null
    rows.push({
      key,
      total: list.length,
      open: open.length,
      closed: closed.length,
      pct: list.length ? closed.length / list.length : 0,
      medianCycle: median(cycle),
      p90Cycle: percentile(cycle, 0.9),
      pulls: pullSet.size,
      merged: [...pullSet.values()].filter((p) => p.mergedAt).length,
      medianMerge: median(pullMergeDays([...pullSet.values()])),
      oldestOpen: open.length ? Math.max(...open.map((i) => (now - Date.parse(i.createdAt)) / DAY)) : null,
      withoutPull: open.filter((i) => !(idx.pullsOf.get(i.number)?.length)).length,
      due,
      overdue: !!due && Date.parse(due) < now && open.length > 0,
    })
  }
  return rows.sort((a, b) => b.total - a.total)
}

export interface EpicRow {
  epic: Issue
  total: number
  closed: number
  open: number
  pct: number // by the real state of the children, not by the boxes of the checklist
  medianCycle: number | null
  oldestOpen: number | null
  withPull: number // children with a pull request behind
  openWithoutPull: number
  blocked: number // open children blocked by something still open
  depth: number // levels of children under it
}

/** One row per issue that has children: how far along they are, and what holds them back. */
export function epicRows(idx: LinkIndex, now: number): EpicRow[] {
  const depthOf = (n: number, seen: Set<number>): number => {
    if (seen.has(n)) return 0
    seen.add(n)
    const kids = idx.childrenOf.get(n) ?? []
    return kids.length ? 1 + Math.max(...kids.map((k) => depthOf(k.number, seen))) : 0
  }
  const rows: EpicRow[] = []
  for (const [n, kids] of idx.childrenOf) {
    const parent = findIssue(idx, n)
    if (!parent) continue
    const children = [...new Map(kids.map((k) => [k.number, k])).values()]
    const closed = children.filter((c) => c.state === "CLOSED")
    const open = children.filter((c) => c.state === "OPEN")
    rows.push({
      epic: parent,
      total: children.length,
      closed: closed.length,
      open: open.length,
      pct: children.length ? closed.length / children.length : 0,
      medianCycle: median(issueCycleDays(closed)),
      oldestOpen: open.length ? Math.max(...open.map((c) => (now - Date.parse(c.createdAt)) / DAY)) : null,
      withPull: children.filter((c) => idx.pullsOf.get(c.number)?.length).length,
      openWithoutPull: open.filter((c) => !idx.pullsOf.get(c.number)?.length).length,
      blocked: open.filter((c) => openBlockers(c.number, idx).length > 0).length,
      depth: depthOf(n, new Set()),
    })
  }
  return rows.sort((a, b) => b.total - a.total)
}

// the issue behind a number that has children: the parents map holds it for every child
function findIssue(idx: LinkIndex, n: number): Issue | null {
  for (const parents of idx.parentsOf.values()) for (const p of parents) if (p.number === n) return p
  return null
}

export interface BlockerRow {
  issue: Issue
  direct: number // open issues it blocks directly
  transitive: number // and the ones held back behind those, all the way down
  blockedBy: number // open issues that block it in turn
}

/** The open issues that hold others back, the ones that hold the most first. */
export function blockerRanking(idx: LinkIndex): BlockerRow[] {
  const rows: BlockerRow[] = []
  for (const [n] of idx.blockedOf) {
    const issue = findBlocker(idx, n)
    if (!issue || issue.state !== "OPEN") continue
    const direct = openBlocked(n, idx)
    if (direct.length === 0) continue
    const seen = new Set<number>([n])
    const stack = [...direct]
    while (stack.length) {
      const cur = stack.pop()!
      if (seen.has(cur.number)) continue
      seen.add(cur.number)
      stack.push(...openBlocked(cur.number, idx))
    }
    rows.push({ issue, direct: direct.length, transitive: seen.size - 1, blockedBy: openBlockers(n, idx).length })
  }
  return rows.sort((a, b) => b.transitive - a.transitive || b.direct - a.direct)
}

function findBlocker(idx: LinkIndex, n: number): Issue | null {
  for (const list of idx.blockersOf.values()) for (const i of list) if (i.number === n) return i
  return null
}

/** Open issues that are blocked right now (by an issue that is still open). */
export function blockedNow(issues: Issue[], idx: LinkIndex) {
  return issues.filter((i) => i.state === "OPEN" && openBlockers(i.number, idx).length > 0)
}
