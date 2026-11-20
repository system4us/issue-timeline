import type { Issue, Link, Pull } from "./types"
import { NONE, assigneesOf, isOverdue, labelsOf, linkIndex, milestonesOf, openBlocked, openBlockers, type LinkIndex } from "./filters"

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

export interface OpenPoint { t: number; open: number; closed: number }

/** How many of the sorted times are <= t, moving a cursor forward (the `t`s must come in increasing order). */
const counter = (sorted: number[]) => {
  let i = 0
  return (t: number) => { while (i < sorted.length && sorted[i]! <= t) i++; return i }
}

/**
 * Issues open (and closed so far) at the end of each week: created so far minus closed so far (reopened issues count as open now).
 * One sort and one pass, so a repo of thousands of issues over years stays cheap.
 */
export function openOverTime(issues: Issue[], until: number): OpenPoint[] {
  const created = issues.map((i) => ms(i.createdAt)).filter((t) => !Number.isNaN(t)).sort((a, b) => a - b)
  if (created.length === 0) return []
  const closed = issues.filter((i) => i.closedAt).map((i) => ms(i.closedAt!)).sort((a, b) => a - b)
  const c = counter(created)
  const d = counter(closed)
  const out: OpenPoint[] = []
  for (let w = weekStart(created[0]!); w <= until; w += 7 * DAY) {
    const end = Math.min(w + 7 * DAY, until)
    const k = d(end)
    out.push({ t: end, open: c(end) - k, closed: k })
  }
  return out
}

export interface CyclePoint { week: number; closed: number; median: number | null; p90: number | null }

/** Per week of closing: how many issues closed (the throughput) and how long the ones closed that week had taken. */
export function cycleTrend(issues: Issue[], until: number): CyclePoint[] {
  const byWeek = new Map<number, number[]>()
  for (const i of issues) {
    if (!i.closedAt) continue
    const w = weekStart(ms(i.closedAt))
    ;(byWeek.get(w) ?? byWeek.set(w, []).get(w)!).push(days(i.createdAt, i.closedAt))
  }
  if (byWeek.size === 0) return []
  const out: CyclePoint[] = []
  for (let w = Math.min(...byWeek.keys()); w <= weekStart(until); w += 7 * DAY) {
    const v = byWeek.get(w) ?? []
    out.push({ week: w, closed: v.length, median: median(v), p90: percentile(v, 0.9) })
  }
  return out
}

/** The headline numbers of the analysis, all from the items on screen. */
export function summary(issues: Issue[], pulls: Pull[], now: number) {
  const cycle = issueCycleDays(issues)
  const merge = pullMergeDays(pulls)
  const last30 = now - 30 * DAY
  const merged = pulls.filter((p) => p.state === "MERGED")
  return {
    total: issues.length,
    open: issues.filter((i) => i.state === "OPEN").length,
    created30: issues.filter((i) => ms(i.createdAt) >= last30).length,
    closed30: issues.filter((i) => i.closedAt && ms(i.closedAt) >= last30).length,
    cycleMedian: median(cycle),
    cycleP90: percentile(cycle, 0.9),
    merged30: mergedBetween(pulls, last30, now).length,
    openPulls: pulls.filter((p) => p.state === "OPEN").length,
    mergeMedian: median(merge),
    mergeP90: percentile(merge, 0.9),
    reviewMedian: median(firstReviewDays(pulls)),
    mergedTotal: merged.length,
    mergedNoReview: merged.filter((p) => p.reviewsCount === 0).length,
  }
}

/** Merged pull requests that nobody reviewed. */
export const mergedWithoutReview = (pulls: Pull[]) => pulls.filter((p) => p.state === "MERGED" && p.reviewsCount === 0)

/** First commit of a pull request (its own commits, when the store has them). */
const firstCommit = (p: Pull) => {
  let first = NaN
  for (const c of p.commits ?? []) { const t = ms(c.at); if (!Number.isNaN(t) && !(t >= first)) first = t }
  return first
}

/**
 * Days from the first commit to the merge, for the merged pull requests whose commits are known.
 * A commit can be older than the PR (work started before opening it), so this is the lead time of the code, not of the PR.
 */
export function commitToMergeDays(pulls: Pull[]): number[] {
  const out: number[] = []
  for (const p of pulls) {
    if (!p.mergedAt) continue
    const t = firstCommit(p)
    if (!Number.isNaN(t)) out.push(Math.max(0, (ms(p.mergedAt) - t) / DAY))
  }
  return out
}

export interface WipRow { item: Issue | Pull; age: number }

/**
 * The work in progress now: the open pull requests, and the open issues that have an open pull request behind them.
 * `age` is in days since the PR (or the issue) was opened, oldest first.
 */
export function workInProgress(issues: Issue[], pulls: Pull[], idx: LinkIndex, now: number) {
  const age = (x: Issue | Pull) => (now - ms(x.createdAt)) / DAY
  const openPulls = pulls.filter((p) => p.state === "OPEN")
  const issuesInProgress = issues.filter((i) => i.state === "OPEN" && (idx.pullsOf.get(i.key) ?? []).some((p) => p.state === "OPEN"))
  const ages = openPulls.map(age)
  return {
    pulls: openPulls.map((p) => ({ item: p, age: age(p) })).sort((a, b) => b.age - a.age) as WipRow[],
    drafts: openPulls.filter((p) => p.isDraft).length,
    issues: issuesInProgress.map((i) => ({ item: i, age: age(i) })).sort((a, b) => b.age - a.age) as WipRow[],
    medianAge: median(ages),
    // how old the open PRs are, in the same buckets as the merge histogram
    bins: histogram(ages, [1, 3, 7, 14, 30, 90]),
  }
}

export interface PersonRow { person: string; opened: number; open: number; merged: number; mergedOthers: number; medianMerge: number | null }

/**
 * Pull requests per person: the ones they opened (still open, merged) and the ones of others they merged.
 * Reviews per person are not in the payload (only in an item's detail), so merging is the review load we can measure.
 */
export function peopleLoad(pulls: Pull[]): PersonRow[] {
  const rows = new Map<string, PersonRow & { merges: number[] }>()
  const row = (p: string) => rows.get(p) ?? rows.set(p, { person: p, opened: 0, open: 0, merged: 0, mergedOthers: 0, medianMerge: null, merges: [] }).get(p)!
  for (const p of pulls) {
    if (p.author) {
      const r = row(p.author)
      r.opened++
      if (p.state === "OPEN") r.open++
      if (p.mergedAt) { r.merged++; r.merges.push(days(p.createdAt, p.mergedAt)) }
    }
    if (p.mergedBy && p.mergedBy !== p.author) row(p.mergedBy).mergedOthers++
  }
  return [...rows.values()]
    .map(({ merges, ...r }) => ({ ...r, medianMerge: median(merges) }))
    .sort((a, b) => b.opened + b.mergedOthers - (a.opened + a.mergedOthers) || a.person.localeCompare(b.person))
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
    withPull: { count: closed.filter((i) => linked.has(i.key)).length, median: median(issueCycleDays(closed.filter((i) => linked.has(i.key)))) },
    withoutPull: { count: closed.filter((i) => !linked.has(i.key)).length, median: median(issueCycleDays(closed.filter((i) => !linked.has(i.key)))) },
    openWithoutPull: issues.filter((i) => i.state === "OPEN" && !linked.has(i.key)).length,
  }
}

/** Size of a pull request against how long it took to merge. */
export function sizeVsMerge(pulls: Pull[]) {
  return pulls
    .filter((p) => p.mergedAt)
    .map((p) => ({ key: p.key, size: p.additions + p.deletions, days: days(p.createdAt, p.mergedAt!) }))
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
    const pullSet = new Map<string, Pull>()
    for (const i of list) for (const p of idx.pullsOf.get(i.key) ?? []) pullSet.set(p.key, p)
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
      withoutPull: open.filter((i) => !(idx.pullsOf.get(i.key)?.length)).length,
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
  const depthOf = (n: string, seen: Set<string>): number => {
    if (seen.has(n)) return 0
    seen.add(n)
    const kids = idx.childrenOf.get(n) ?? []
    return kids.length ? 1 + Math.max(...kids.map((k) => depthOf(k.key, seen))) : 0
  }
  const rows: EpicRow[] = []
  const parents = issuesIn(idx.parentsOf)
  for (const [n, kids] of idx.childrenOf) {
    const parent = parents.get(n)
    if (!parent) continue
    const children = [...new Map(kids.map((k) => [k.key, k])).values()]
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
      withPull: children.filter((c) => idx.pullsOf.get(c.key)?.length).length,
      openWithoutPull: open.filter((c) => !idx.pullsOf.get(c.key)?.length).length,
      blocked: open.filter((c) => openBlockers(c.key, idx).length > 0).length,
      depth: depthOf(n, new Set()),
    })
  }
  return rows.sort((a, b) => b.total - a.total)
}

// The issues a map of lists holds, by key: the parents map holds every issue that has children, the blockers map every blocker.
// Built once, instead of a scan of the whole map for each row.
function issuesIn(m: Map<string, Issue[]>): Map<string, Issue> {
  const out = new Map<string, Issue>()
  for (const list of m.values()) for (const i of list) out.set(i.key, i)
  return out
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
  const blockers = issuesIn(idx.blockersOf)
  for (const [n] of idx.blockedOf) {
    const issue = blockers.get(n)
    if (!issue || issue.state !== "OPEN") continue
    const direct = openBlocked(n, idx)
    if (direct.length === 0) continue
    const seen = new Set<string>([n])
    const stack = [...direct]
    while (stack.length) {
      const cur = stack.pop()!
      if (seen.has(cur.key)) continue
      seen.add(cur.key)
      stack.push(...openBlocked(cur.key, idx))
    }
    rows.push({ issue, direct: direct.length, transitive: seen.size - 1, blockedBy: openBlockers(n, idx).length })
  }
  return rows.sort((a, b) => b.transitive - a.transitive || b.direct - a.direct)
}

/** Open issues that are blocked right now (by an issue that is still open). */
export function blockedNow(issues: Issue[], idx: LinkIndex) {
  return issues.filter((i) => i.state === "OPEN" && openBlockers(i.key, idx).length > 0)
}

export { isOverdue } from "./filters"

/** The open issues whose own due date has passed, the most days late first. `late` is in days. */
export function overdueIssues(issues: Issue[], now: number) {
  return issues
    .filter((i) => isOverdue(i, now))
    .map((i) => ({ issue: i, late: Math.floor((now - ms(i.dueDate!)) / DAY) }))
    .sort((a, b) => b.late - a.late)
}

export type Gap = "assignee" | "milestone" | "dueDate" | "labels"

export const GAP_LABEL: Record<Gap, string> = { assignee: "No assignee", milestone: "No milestone", dueDate: "No due date", labels: "No labels" }

/** The gaps of one open issue. A due date is asked for only when the repo uses them at all (a tracker without them would flag everything). */
export const gapsOf = (i: Issue, usesDue: boolean): Gap[] => {
  const g: Gap[] = []
  if (i.assignees.length === 0) g.push("assignee")
  if (!i.milestone) g.push("milestone")
  if (usesDue && !i.dueDate) g.push("dueDate")
  if (i.labels.length === 0) g.push("labels")
  return g
}

/** Open issues that lack a field, and how many lack each: what a planning view needs filled in first. */
export function dataQuality(issues: Issue[]) {
  const open = issues.filter((i) => i.state === "OPEN")
  const usesDue = issues.some((i) => !!i.dueDate)
  const byGap: Record<Gap, number> = { assignee: 0, milestone: 0, dueDate: 0, labels: 0 }
  const incomplete: { issue: Issue; gaps: Gap[] }[] = []
  for (const issue of open) {
    const gaps = gapsOf(issue, usesDue)
    for (const g of gaps) byGap[g]++
    if (gaps.length) incomplete.push({ issue, gaps })
  }
  incomplete.sort((a, b) => b.gaps.length - a.gaps.length || ms(a.issue.createdAt) - ms(b.issue.createdAt))
  return { open: open.length, usesDue, byGap, incomplete }
}

export interface Forecast {
  remaining: number
  /** Issues closed in each of the last full weeks, oldest first. */
  closedWeekly: number[]
  /** Issues created in the same weeks. */
  createdWeekly: number[]
  /** Weeks to clear what is open at the pace of the slow (p25), usual (median) and fast (p75) weeks; null when that pace is zero. */
  weeks: { slow: number | null; usual: number | null; fast: number | null }
  /** The same as dates (ms). */
  dates: { slow: number | null; usual: number | null; fast: number | null }
  /** True when more is created than closed in the usual week: the backlog grows, so the dates hold only if nothing new arrives. */
  growing: boolean
}

/**
 * When the open issues on screen would be done at the pace of the last `weeks` full weeks. Filtered by milestone or epic, it is that
 * one's burndown. The dates assume nothing new is created; `growing` says when that does not hold.
 */
export function forecast(issues: Issue[], now: number, weeks = 8): Forecast {
  const thisWeek = weekStart(now)
  const first = thisWeek - weeks * 7 * DAY
  const closedWeekly = new Array<number>(weeks).fill(0)
  const createdWeekly = new Array<number>(weeks).fill(0)
  const slot = (iso: string | null) => {
    const t = ms(iso)
    return Number.isNaN(t) || t < first || t >= thisWeek ? -1 : Math.floor((t - first) / (7 * DAY))
  }
  for (const i of issues) {
    const c = slot(i.closedAt)
    if (c >= 0) closedWeekly[c]!++
    const k = slot(i.createdAt)
    if (k >= 0) createdWeekly[k]!++
  }
  const remaining = issues.filter((i) => i.state === "OPEN").length
  const pace = (p: number) => percentile(closedWeekly, p)
  const toWeeks = (rate: number | null) => (rate && rate > 0 ? remaining / rate : null)
  const w = { slow: toWeeks(pace(0.25)), usual: toWeeks(pace(0.5)), fast: toWeeks(pace(0.75)) }
  const at = (x: number | null) => (x === null ? null : now + x * 7 * DAY)
  return {
    remaining, closedWeekly, createdWeekly, weeks: w,
    dates: { slow: at(w.slow), usual: at(w.usual), fast: at(w.fast) },
    growing: (median(createdWeekly) ?? 0) > (median(closedWeekly) ?? 0),
  }
}

export interface AgingRow { issue: Issue; age: number; idle: number; ratio: number }

/**
 * Open issues older than what closed issues usually took (the p85 of their cycle), oldest relative to that first. `idle` is days since the
 * last update. Needs at least 5 closed issues to know what usual is; otherwise `limit` is null and nothing is flagged.
 */
export function wipAging(issues: Issue[], now: number): { limit: number | null; rows: AgingRow[]; open: number } {
  const open = issues.filter((i) => i.state === "OPEN")
  const cycle = issueCycleDays(issues)
  const limit = cycle.length >= 5 ? percentile(cycle, 0.85) : null
  if (limit === null || limit <= 0) return { limit: null, rows: [], open: open.length }
  const rows = open
    .map((issue) => ({ issue, age: (now - ms(issue.createdAt)) / DAY, idle: (now - ms(issue.updatedAt)) / DAY, ratio: 0 }))
    .map((r) => ({ ...r, ratio: r.age / limit }))
    .filter((r) => r.age > limit)
    .sort((a, b) => b.ratio - a.ratio)
  return { limit, rows, open: open.length }
}
