import { useMemo, type ReactNode } from "react"
import type { DepEdge, Issue, Item, Link, Pull, TreeEdge } from "./types"
import { NONE, applyFilters, type Filters, type LinkIndex } from "./filters"
import {
  DAY, GAP_LABEL, blockedNow, blockerRanking, breakdown, commitToMergeDays, cycleTrend, dataQuality, forecast, gapsOf, wipAging, days as spanDays, epicRows, firstReviewDays, histogram,
  issueCycleDays, issuesWithAndWithoutPull, labelCounts, mergedWithoutReview, oldestOpen, openOverTime, overdueIssues, peopleLoad,
  perWeek, pullMergeDays, sizeVsMerge, summary, weekStart, workInProgress, type Bin, type GroupBy,
} from "./metrics"
import { CycleTrend, Empty, Histogram, OpenLine, Scatter, WeeklyBars } from "./charts"
import type { CiSummary } from "./CiBadge"
import { byKey, refOf } from "./keys"
import type { Size, WidgetMeta } from "./dashboard"

/** What every widget is drawn from: the items on screen (the global filters applied) and how to act on them. */
export interface Ctx {
  issues: Issue[]
  pulls: Pull[]
  links: Link[]
  tree: TreeEdge[]
  deps: DepEdge[]
  all: { issues: Issue[]; pulls: Pull[] }
  filters: Filters
  ci?: CiSummary
  hasCode: boolean
  now: number
  from: number // start of the series window (0: all)
  idx: LinkIndex // tree, dependencies and PRs of everything (a child outside the filter still counts for its epic)
  pool: Pool
  onFilters: (p: Partial<Filters>) => void
  onSelect: (i: Item) => void
  /** Opens the detailed list above the dashboard; `from` is read again from the current pool, so the list follows the filters. */
  list: (title: string, from: (p: Pool) => Item[], epic?: string) => void
  pickedEpic?: string
}

export interface Pool { issues: Issue[]; pulls: Pull[]; idx: LinkIndex; linked: Set<string> }

export interface Csv { file: string; headers: string[]; rows: (string | number | boolean | null)[][] }

export interface Widget extends WidgetMeta {
  title: string
  hint?: string // a short line next to the title: what a click does, what it counts
  needs?: "code" | "ci" // a linked code repo (PRs), or CI runs other than a PR's
  Body: (p: { ctx: Ctx; opts: Record<string, string>; setOpts: (o: Record<string, string>) => void }) => ReactNode
  csv?: (ctx: Ctx, opts: Record<string, string>) => Csv
}

export const available = (w: Widget, ctx: Pick<Ctx, "hasCode" | "ci">) =>
  w.needs === "code" ? ctx.hasCode : w.needs === "ci" ? ctx.hasCode && !!ctx.ci && Object.keys(ctx.ci.other).length > 0 : true

// ——— helpers ———

export const fmt = (v: number | null, unit = "d") => (v === null ? "—" : `${v < 10 ? v.toFixed(1) : Math.round(v)} ${unit}`)
const round1 = (v: number | null) => (v === null ? null : Math.round(v * 10) / 10)
const dayOf = (t: number) => new Date(t).toISOString().slice(0, 10)
const inWeek = (iso: string | null, w: number) => { const t = iso ? Date.parse(iso) : NaN; return t >= w && t < w + 7 * DAY }
const age = (d: number | null) => (d === null ? "—" : `${Math.round(d)} d`)

const childrenSorted = (idx: LinkIndex, n: string): Issue[] =>
  [...new Map((idx.childrenOf.get(n) ?? []).map((k) => [k.key, k])).values()]
    .sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED") || byKey(a.key, b.key))

const binTitle = (what: string, b: Bin) => `${what}: ${b.to === Infinity ? `${b.from} days or more` : `${b.from} to ${b.to} days`}`
const inBin = (b: Bin) => (d: number | null) => d !== null && d >= b.from && d < b.to

function Ref({ item, ctx }: { item: Item; ctx: Ctx }) {
  return <button className="link-btn" onClick={(e) => { e.stopPropagation(); ctx.onSelect(item) }}>{refOf(item.key)}</button>
}

function Kpi({ value, hint, onClick, tone }: { value: string; hint?: string; onClick?: () => void; tone?: "warn" | "error" }) {
  const body = <><div className={`kpi-value${tone ? ` ${tone}` : ""}`}>{value}</div>{hint && <div className="muted small">{hint}</div>}</>
  return onClick ? <button className="kpi clickable" onClick={onClick} title="Click to list them">{body}</button> : <div className="kpi">{body}</div>
}

const useSummary = (ctx: Ctx) => useMemo(() => summary(ctx.issues, ctx.pulls, ctx.now), [ctx.issues, ctx.pulls, ctx.now])

const weekly = (series: ReturnType<typeof perWeek>, from: number) => series.filter((w) => w.week >= weekStart(from))

const GROUPS: { value: GroupBy; label: string }[] = [
  { value: "milestone", label: "Milestone" }, { value: "etiqueta", label: "Label" }, { value: "autor", label: "Author" }, { value: "asignado", label: "Assignee" },
]
const groupOf = (opts: Record<string, string>): GroupBy => (GROUPS.some((g) => g.value === opts.groupBy) ? (opts.groupBy as GroupBy) : "milestone")
const FIELD = { milestone: "milestones", etiqueta: "labels", autor: "authors", asignado: "assignees" } as const

// The table compares every group, so it ignores the filter of its own dimension (picking a milestone must not hide the others),
// and the state filter too: a group's progress is its closed share, which an "open only" view would always show as 0%.
const breakdownRows = (ctx: Ctx, by: GroupBy) => {
  const base = applyFilters(ctx.all.issues, ctx.all.pulls, ctx.links, { ...ctx.filters, [FIELD[by]]: [], state: "all" }, ctx.tree, ctx.deps)
  return breakdown(base.issues, ctx.links, base.pulls, by, ctx.now)
}
const epicsOf = (ctx: Ctx) => { const shown = new Set(ctx.issues.map((i) => i.key)); return epicRows(ctx.idx, ctx.now).filter((r) => shown.has(r.epic.key)) }

const OTHER_LABEL: Record<string, string> = { dependabot: "Dependabot", pages: "Pages deployments", other: "Releases, tags and other workflows" }

const kpi = (id: string, title: string, Body: Widget["Body"], needs?: Widget["needs"]): Widget => ({ id, title, w: 2, min: 2, needs, Body })
const S = (n: Size) => n

// ——— the catalog ———

export const CATALOG: Widget[] = [
  kpi("kpi-open", "Open issues", ({ ctx }) => {
    const s = useSummary(ctx)
    return <Kpi value={String(s.open)} hint={`of ${s.total} · click to list`} onClick={() => ctx.list("Open issues", (p) => p.issues.filter((i) => i.state === "OPEN"))} />
  }),
  kpi("kpi-flow30", "Created / closed (30 d)", ({ ctx }) => {
    const s = useSummary(ctx)
    return <Kpi value={`${s.created30} / ${s.closed30}`} hint={s.created30 > s.closed30 ? "the backlog grows" : "the backlog shrinks or holds"} tone={s.created30 > s.closed30 ? "warn" : undefined} />
  }),
  kpi("kpi-cycle", "Median time to close an issue", ({ ctx }) => {
    const s = useSummary(ctx)
    return <Kpi value={fmt(s.cycleMedian)} hint={`p90 ${fmt(s.cycleP90)}`} />
  }),
  kpi("kpi-merged30", "PRs merged (30 d)", ({ ctx }) => {
    const s = useSummary(ctx)
    return <Kpi value={String(s.merged30)} hint={`${s.openPulls} open now`} />
  }, "code"),
  kpi("kpi-merge", "Median time to merge", ({ ctx }) => {
    const s = useSummary(ctx)
    return <Kpi value={fmt(s.mergeMedian)} hint={`p90 ${fmt(s.mergeP90)} · 1st review ${fmt(s.reviewMedian)}`} />
  }, "code"),
  kpi("kpi-noreview", "PRs merged without review", ({ ctx }) => {
    const s = useSummary(ctx)
    return <Kpi value={String(s.mergedNoReview)} hint={`of ${s.mergedTotal} merged · click to list`} tone={s.mergedNoReview ? "warn" : undefined}
      onClick={() => ctx.list("PRs merged without review", (p) => mergedWithoutReview(p.pulls))} />
  }, "code"),
  kpi("kpi-wip", "Work in progress", ({ ctx }) => {
    const w = useMemo(() => workInProgress(ctx.issues, ctx.pulls, ctx.idx, ctx.now), [ctx.issues, ctx.pulls, ctx.idx, ctx.now])
    return <Kpi value={String(w.pulls.length)} hint={`open PRs · ${w.drafts} drafts · median age ${fmt(w.medianAge)}`}
      onClick={() => ctx.list("Open PRs", (p) => p.pulls.filter((x) => x.state === "OPEN"))} />
  }, "code"),

  {
    id: "breakdown", title: "Progress by group", hint: "the other filters apply, not the state · a click on a row filters everything by it", w: S(12), min: S(6),
    Body: ({ ctx, opts, setOpts }) => {
      const by = groupOf(opts)
      const field = FIELD[by]
      const rows = useMemo(() => breakdownRows(ctx, by), [ctx.all, ctx.links, ctx.tree, ctx.deps, ctx.filters, by, ctx.now])
      const pick = (key: string) => { const cur = ctx.filters[field]; ctx.onFilters({ [field]: cur.includes(key) ? cur.filter((k) => k !== key) : [...cur, key] } as Partial<Filters>) }
      return (
        <>
          <div className="widget-tools">
            <label>Group by <select value={by} onChange={(e) => setOpts({ groupBy: e.target.value })}>{GROUPS.map((g) => <option key={g.value} value={g.value}>{g.label}</option>)}</select></label>
            <span className="muted small">{rows.length} groups</span>
          </div>
          {rows.length === 0 ? <Empty /> : (
            <div className="scroll-x">
              <table className="table breakdown">
                <thead><tr><th>{GROUPS.find((g) => g.value === by)!.label}</th><th className="num">Issues</th><th>Progress</th><th className="num">Open</th><th className="num">Median close</th><th className="num">p90</th>{ctx.hasCode && <><th className="num">PRs</th><th className="num">Median merge</th><th className="num">Open without PR</th></>}<th className="num">Oldest</th>{by === "milestone" && <th>Due</th>}</tr></thead>
                <tbody>
                  {rows.map((r) => (
                    <tr key={r.key} className={ctx.filters[field].includes(r.key) ? "picked" : ""} onClick={() => pick(r.key)}>
                      <td className="wrap">{r.key === NONE ? <i className="muted">(none)</i> : r.key}</td>
                      <td className="num">{r.total}</td>
                      <td><div className="progress-cell"><div className="progress"><div style={{ width: `${r.pct * 100}%` }} /></div><span>{Math.round(r.pct * 100)}%</span></div></td>
                      <td className="num">{r.open}</td>
                      <td className="num">{fmt(r.medianCycle)}</td>
                      <td className="num">{fmt(r.p90Cycle)}</td>
                      {ctx.hasCode && <>
                        <td className="num">{r.merged}/{r.pulls}</td>
                        <td className="num">{fmt(r.medianMerge)}</td>
                        <td className={`num${r.withoutPull && r.open === r.withoutPull ? " warn" : ""}`}>{r.withoutPull}</td>
                      </>}
                      <td className="num">{age(r.oldestOpen)}</td>
                      {by === "milestone" && <td className={r.overdue ? "error" : ""}>{r.due ? r.due.slice(0, 10) : "—"}{r.overdue ? " · overdue" : ""}</td>}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )
    },
    csv: (ctx, opts) => {
      const by = groupOf(opts)
      return {
        file: `breakdown-${by}.csv`,
        headers: [GROUPS.find((g) => g.value === by)!.label, "Issues", "Closed", "Open", "Progress %", "Median close (d)", "p90 close (d)", "PRs", "Merged PRs", "Median merge (d)", "Open without PR", "Oldest open (d)", "Due", "Overdue"],
        rows: breakdownRows(ctx, by).map((r) => [r.key === NONE ? "(none)" : r.key, r.total, r.closed, r.open, Math.round(r.pct * 100), round1(r.medianCycle), round1(r.p90Cycle), r.pulls, r.merged, round1(r.medianMerge), r.withoutPull, r.oldestOpen === null ? null : Math.round(r.oldestOpen), r.due ? r.due.slice(0, 10) : null, r.overdue]),
      }
    },
  },

  {
    id: "epics", title: "Epics and issues with children", hint: "a click lists the children · progress counts the real state of each child", w: S(6), min: S(4),
    Body: ({ ctx }) => {
      const epics = useMemo(() => epicsOf(ctx), [ctx.idx, ctx.issues, ctx.now])
      if (epics.length === 0) return <Empty>No issue on screen has children.</Empty>
      return (
        <div className="scroll-x">
          <table className="table breakdown">
            <thead><tr><th>Epic</th><th>Progress</th><th className="num">Open</th>{ctx.hasCode && <th className="num">Without PR</th>}<th className="num">Blocked</th><th className="num">Median close</th><th className="num">Oldest</th></tr></thead>
            <tbody>
              {epics.map((r) => (
                <tr key={r.epic.key} className={ctx.pickedEpic === r.epic.key ? "picked" : ""}
                  onClick={() => ctx.list(`Children of ${refOf(r.epic.key)} ${r.epic.title}`, (p) => childrenSorted(p.idx, r.epic.key), r.epic.key)}>
                  <td className="wrap title-cell"><Ref item={r.epic} ctx={ctx} /> {r.epic.title}</td>
                  <td><div className="progress-cell"><div className="progress"><div style={{ width: `${r.pct * 100}%` }} /></div><span>{r.closed}/{r.total}</span></div></td>
                  <td className="num">{r.open}</td>
                  {ctx.hasCode && <td className={`num${r.openWithoutPull ? " warn" : ""}`}>{r.openWithoutPull}</td>}
                  <td className={`num${r.blocked ? " error" : ""}`}>{r.blocked}</td>
                  <td className="num">{fmt(r.medianCycle)}</td>
                  <td className="num">{age(r.oldestOpen)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )
    },
    csv: (ctx) => ({
      file: "epics.csv",
      headers: ["Epic", "Title", "Children", "Closed", "Open", "Progress %", "With PR", "Open without PR", "Blocked", "Median close (d)", "Oldest open (d)"],
      rows: epicsOf(ctx).map((r) => [r.epic.key, r.epic.title, r.total, r.closed, r.open, Math.round(r.pct * 100), r.withPull, r.openWithoutPull, r.blocked, round1(r.medianCycle), r.oldestOpen === null ? null : Math.round(r.oldestOpen)]),
    }),
  },

  {
    id: "blockers", title: "Blockers", hint: "open issues that hold others back, the most first", w: S(6), min: S(4),
    Body: ({ ctx }) => {
      const blockers = useMemo(() => blockerRanking(ctx.idx), [ctx.idx])
      const blocked = useMemo(() => blockedNow(ctx.issues, ctx.idx), [ctx.issues, ctx.idx])
      if (blockers.length === 0) return <Empty>No open issue blocks another open one.</Empty>
      return (
        <>
          <div className="scroll-x">
            <table className="table breakdown">
              <thead><tr><th>Open blocker</th><th className="num">Blocks</th><th className="num">Chained</th><th className="num">Itself blocked by</th></tr></thead>
              <tbody>
                {blockers.slice(0, 12).map((b) => (
                  <tr key={b.issue.key} onClick={() => ctx.onSelect(b.issue)}>
                    <td className="wrap title-cell"><Ref item={b.issue} ctx={ctx} /> {b.issue.title}</td>
                    <td className="num">{b.direct}</td>
                    <td className={`num${b.transitive > b.direct ? " warn" : ""}`}>{b.transitive}</td>
                    <td className={`num${b.blockedBy ? " error" : ""}`}>{b.blockedBy}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {blocked.length > 0 && (
            <p className="small muted">
              {blocked.length} blocked now: {blocked.slice(0, 10).map((i, k) => <span key={i.key}>{k ? ", " : ""}<Ref item={i} ctx={ctx} /></span>)}{blocked.length > 10 ? "…" : ""}
              {" "}<button className="link-btn" onClick={() => ctx.list("Blocked now", (p) => blockedNow(p.issues, p.idx))}>list all</button>
            </p>
          )}
        </>
      )
    },
    csv: (ctx) => ({
      file: "blockers.csv",
      headers: ["Issue", "Title", "Blocks directly", "Blocks in the chain", "Blocked by open issues"],
      rows: blockerRanking(ctx.idx).map((b) => [b.issue.key, b.issue.title, b.direct, b.transitive, b.blockedBy]),
    }),
  },

  {
    id: "open-over-time", title: "Open issues over time", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const pts = useMemo(() => openOverTime(ctx.issues, ctx.now).filter((p) => p.t >= ctx.from), [ctx.issues, ctx.now, ctx.from])
      return <OpenLine points={pts} />
    },
  },
  {
    id: "cfd", title: "Cumulative flow", hint: "open on the bottom, closed so far on top: the top line is everything created", w: S(6), min: S(3),
    Body: ({ ctx }) => {
      const pts = useMemo(() => openOverTime(ctx.issues, ctx.now).filter((p) => p.t >= ctx.from), [ctx.issues, ctx.now, ctx.from])
      return <OpenLine points={pts} stacked />
    },
  },
  {
    id: "issues-weekly", title: "Issues created and closed per week", hint: "click a week to list it", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const weeks = useMemo(() => weekly(perWeek(ctx.issues.map((i) => i.createdAt), ctx.issues.map((i) => i.closedAt), ctx.now), ctx.from), [ctx.issues, ctx.now, ctx.from])
      return <WeeklyBars weeks={weeks} aLabel="created" bLabel="closed"
        onPick={(w) => ctx.list(`Issues created or closed in the week of ${dayOf(w)}`, (p) => p.issues.filter((i) => inWeek(i.createdAt, w) || inWeek(i.closedAt, w)))} />
    },
  },
  {
    id: "pulls-weekly", title: "PRs opened and merged per week", hint: "click a week to list it", w: S(4), min: S(3), needs: "code",
    Body: ({ ctx }) => {
      const weeks = useMemo(() => weekly(perWeek(ctx.pulls.map((p) => p.createdAt), ctx.pulls.map((p) => p.mergedAt), ctx.now), ctx.from), [ctx.pulls, ctx.now, ctx.from])
      return <WeeklyBars weeks={weeks} aLabel="opened" bLabel="merged"
        onPick={(w) => ctx.list(`PRs opened or merged in the week of ${dayOf(w)}`, (p) => p.pulls.filter((x) => inWeek(x.createdAt, w) || inWeek(x.mergedAt, w)))} />
    },
  },
  {
    id: "cycle-trend", title: "Throughput and cycle time", hint: "per week of closing · click a week to list what closed", w: S(6), min: S(3),
    Body: ({ ctx }) => {
      const pts = useMemo(() => cycleTrend(ctx.issues, ctx.now).filter((p) => p.week >= weekStart(ctx.from)), [ctx.issues, ctx.now, ctx.from])
      return <CycleTrend points={pts} onPick={(w) => ctx.list(`Issues closed in the week of ${dayOf(w)}`, (p) => p.issues.filter((i) => inWeek(i.closedAt, w)))} />
    },
    csv: (ctx) => ({
      file: "cycle-trend.csv",
      headers: ["Week", "Closed", "Median close (d)", "p90 close (d)"],
      rows: cycleTrend(ctx.issues, ctx.now).filter((p) => p.week >= weekStart(ctx.from)).map((p) => [dayOf(p.week), p.closed, round1(p.median), round1(p.p90)]),
    }),
  },
  {
    id: "close-hist", title: "Days to close an issue", hint: "click a bar to list those issues", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const bins = useMemo(() => histogram(issueCycleDays(ctx.issues), [1, 3, 7, 14, 30, 90]), [ctx.issues])
      return <Histogram bins={bins} unit="days" onPick={(b) => ctx.list(binTitle("Issues closed in", b), (p) => p.issues.filter((i) => inBin(b)(i.closedAt ? spanDays(i.createdAt, i.closedAt) : null)))} />
    },
  },
  {
    id: "merge-hist", title: "Days to merge a PR", hint: "click a bar to list those PRs", w: S(4), min: S(3), needs: "code",
    Body: ({ ctx }) => {
      const bins = useMemo(() => histogram(pullMergeDays(ctx.pulls), [0.1, 0.5, 1, 3, 7, 14]), [ctx.pulls])
      return <Histogram bins={bins} unit="days" onPick={(b) => ctx.list(binTitle("PRs merged in", b), (p) => p.pulls.filter((x) => inBin(b)(x.mergedAt ? spanDays(x.createdAt, x.mergedAt) : null)))} />
    },
  },
  {
    id: "review-hist", title: "Days from opening to first review", hint: "click a bar to list those PRs", w: S(4), min: S(3), needs: "code",
    Body: ({ ctx }) => {
      const vals = useMemo(() => firstReviewDays(ctx.pulls), [ctx.pulls])
      if (vals.length === 0) return <Empty>No PR on screen got a review.</Empty>
      return <Histogram bins={histogram(vals, [0.1, 0.5, 1, 3, 7, 14])} unit="days"
        onPick={(b) => ctx.list(binTitle("PRs first reviewed in", b), (p) => p.pulls.filter((x) => inBin(b)(x.firstReviewAt ? spanDays(x.createdAt, x.firstReviewAt) : null)))} />
    },
  },
  {
    id: "commit-merge-hist", title: "Days from first commit to merge", hint: "the code's lead time: a commit can be older than its PR", w: S(4), min: S(3), needs: "code",
    Body: ({ ctx }) => {
      const vals = useMemo(() => commitToMergeDays(ctx.pulls), [ctx.pulls])
      if (vals.length === 0) return <Empty>No merged PR on screen has its commits read.</Empty>
      return <Histogram bins={histogram(vals, [0.1, 0.5, 1, 3, 7, 14])} unit="days"
        onPick={(b) => ctx.list(binTitle("PRs merged after a first commit", b), (p) => p.pulls.filter((x) => { const [d] = commitToMergeDays([x]); return inBin(b)(d ?? null) }))} />
    },
  },
  {
    id: "size-merge", title: "PR size vs merge time", w: S(4), min: S(3), needs: "code",
    Body: ({ ctx }) => {
      const pts = useMemo(() => sizeVsMerge(ctx.pulls), [ctx.pulls])
      return <Scatter points={pts} onPick={(k) => { const p = ctx.pulls.find((x) => x.key === k); if (p) ctx.onSelect(p) }} />
    },
  },
  {
    id: "wip", title: "Work in progress and its age", hint: "open PRs, oldest first, and the open issues with an open PR behind", w: S(6), min: S(4), needs: "code",
    Body: ({ ctx }) => {
      const w = useMemo(() => workInProgress(ctx.issues, ctx.pulls, ctx.idx, ctx.now), [ctx.issues, ctx.pulls, ctx.idx, ctx.now])
      if (w.pulls.length === 0) return <Empty>No open PR on screen.</Empty>
      return (
        <>
          <p className="small muted">{w.pulls.length} open PRs ({w.drafts} drafts), median age {fmt(w.medianAge)} · {w.issues.length} issues in progress{" "}
            {w.issues.length > 0 && <button className="link-btn" onClick={() => ctx.list("Issues in progress (an open PR behind)", (p) => workInProgress(p.issues, p.pulls, p.idx, ctx.now).issues.map((r) => r.item))}>list them</button>}
          </p>
          <div className="scroll-x">
            <table className="table breakdown">
              <thead><tr><th>PR</th><th className="num">Age</th><th>Author</th></tr></thead>
              <tbody>
                {w.pulls.slice(0, 10).map(({ item, age: a }) => (
                  <tr key={item.key} onClick={() => ctx.onSelect(item)}>
                    <td className="wrap title-cell"><Ref item={item} ctx={ctx} /> {item.title}{(item as Pull).isDraft && <span className="muted small"> · draft</span>}</td>
                    <td className={`num${a > 14 ? " warn" : ""}`}>{Math.round(a)} d</td>
                    <td>{item.author ?? "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )
    },
    csv: (ctx) => ({
      file: "work-in-progress.csv",
      headers: ["Kind", "Key", "Title", "Age (d)", "Author", "Draft"],
      rows: (() => { const w = workInProgress(ctx.issues, ctx.pulls, ctx.idx, ctx.now); return [...w.pulls, ...w.issues].map(({ item, age: a }) => [item.kind, item.key, item.title, Math.round(a), item.author, item.kind === "pull" ? item.isDraft : null]) })(),
    }),
  },
  {
    id: "people", title: "PRs per person", hint: "opened, merged, and others' PRs they merged (reviews per person need the server: not in the data yet)", w: S(6), min: S(4), needs: "code",
    Body: ({ ctx }) => {
      const rows = useMemo(() => peopleLoad(ctx.pulls), [ctx.pulls])
      if (rows.length === 0) return <Empty />
      return (
        <div className="scroll-x">
          <table className="table breakdown">
            <thead><tr><th>Person</th><th className="num">Opened</th><th className="num">Open now</th><th className="num">Merged</th><th className="num">Merged others'</th><th className="num">Median merge</th></tr></thead>
            <tbody>
              {rows.slice(0, 15).map((r) => (
                <tr key={r.person} onClick={() => ctx.list(`PRs by ${r.person} or merged by them`, (p) => p.pulls.filter((x) => x.author === r.person || x.mergedBy === r.person))}>
                  <td>{r.person}</td><td className="num">{r.opened}</td><td className="num">{r.open}</td><td className="num">{r.merged}</td><td className="num">{r.mergedOthers}</td><td className="num">{fmt(r.medianMerge)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )
    },
    csv: (ctx) => ({
      file: "prs-per-person.csv",
      headers: ["Person", "Opened", "Open now", "Merged", "Merged others'", "Median merge (d)"],
      rows: peopleLoad(ctx.pulls).map((r) => [r.person, r.opened, r.open, r.merged, r.mergedOthers, round1(r.medianMerge)]),
    }),
  },
  {
    id: "cross", title: "Issues ↔ PRs cross-check", hint: "click a row to list it", w: S(4), min: S(3), needs: "code",
    Body: ({ ctx }) => {
      const c = useMemo(() => issuesWithAndWithoutPull(ctx.issues, ctx.links), [ctx.issues, ctx.links])
      return (
        <table className="table breakdown">
          <tbody>
            <tr onClick={() => ctx.list("Closed issues with a PR behind", (p) => p.issues.filter((i) => i.closedAt && p.linked.has(i.key)))}><td className="wrap">Closed issues with a PR behind</td><td className="num">{c.withPull.count}</td><td className="num muted">median {fmt(c.withPull.median)}</td></tr>
            <tr onClick={() => ctx.list("Closed issues without a PR", (p) => p.issues.filter((i) => i.closedAt && !p.linked.has(i.key)))}><td className="wrap">Closed issues without a PR</td><td className="num">{c.withoutPull.count}</td><td className="num muted">median {fmt(c.withoutPull.median)}</td></tr>
            <tr onClick={() => ctx.list("Open issues with no PR", (p) => p.issues.filter((i) => i.state === "OPEN" && !p.linked.has(i.key)))}><td className="wrap">Open issues with no PR</td><td className="num">{c.openWithoutPull}</td><td /></tr>
          </tbody>
        </table>
      )
    },
  },
  {
    id: "oldest", title: "Oldest open issues", hint: "click one to open it", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const rows = useMemo(() => oldestOpen(ctx.issues, ctx.now, 8), [ctx.issues, ctx.now])
      if (rows.length === 0) return <Empty>No open issue on screen.</Empty>
      return (
        <table className="table breakdown">
          <tbody>
            {rows.map(({ issue, age: a }) => (
              <tr key={issue.key} onClick={() => ctx.onSelect(issue)}><td className="wrap title-cell"><Ref item={issue} ctx={ctx} /> {issue.title}</td><td className="num">{Math.round(a)} d</td></tr>
            ))}
          </tbody>
        </table>
      )
    },
  },
  {
    id: "overdue", title: "Overdue issues", hint: "open, past their own due date", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const rows = useMemo(() => overdueIssues(ctx.issues, ctx.now), [ctx.issues, ctx.now])
      if (rows.length === 0) return <Empty>None: every open issue with a due date is still ahead of it.</Empty>
      return (
        <>
          <table className="table breakdown">
            <tbody>
              {rows.slice(0, 12).map(({ issue, late }) => (
                <tr key={issue.key} onClick={() => ctx.onSelect(issue)}>
                  <td className="wrap title-cell"><Ref item={issue} ctx={ctx} /> {issue.title}</td>
                  <td className="muted">due {issue.dueDate!.slice(0, 10)}</td>
                  <td className="num error">{late} d late</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length > 12 && <p className="small muted">{rows.length} in all · <button className="link-btn" onClick={() => ctx.list("Overdue issues", (p) => overdueIssues(p.issues, ctx.now).map((r) => r.issue))}>list all</button></p>}
        </>
      )
    },
  },
  {
    id: "forecast", title: "Forecast", hint: "when the open issues on screen finish at the pace of the last 8 weeks; filter by milestone or epic for its burndown", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const f = useMemo(() => forecast(ctx.issues, ctx.now), [ctx.issues, ctx.now])
      if (f.remaining === 0) return <Empty>Nothing open on screen.</Empty>
      if (f.dates.usual === null) return <Empty>No issue was closed in the last 8 weeks, so there is no pace to project from.</Empty>
      const date = (t: number | null) => (t === null ? "never" : dayOf(t))
      return (
        <>
          <table className="table breakdown">
            <tbody>
              <tr><td>Fast weeks (p75)</td><td className="num">{date(f.dates.fast)}</td><td className="num muted">{fmt(f.weeks.fast, "wk")}</td></tr>
              <tr><td><strong>Usual week</strong></td><td className="num"><strong>{date(f.dates.usual)}</strong></td><td className="num muted">{fmt(f.weeks.usual, "wk")}</td></tr>
              <tr><td>Slow weeks (p25)</td><td className="num">{date(f.dates.slow)}</td><td className="num muted">{fmt(f.weeks.slow, "wk")}</td></tr>
            </tbody>
          </table>
          <p className="small muted">{f.remaining} open · closed per week: {f.closedWeekly.join(", ")} · created: {f.createdWeekly.join(", ")}</p>
          {f.growing && <p className="small warn">More is created than closed in a usual week: these dates hold only if nothing new arrives.</p>}
        </>
      )
    },
    csv: (ctx) => {
      const f = forecast(ctx.issues, ctx.now)
      return { file: "forecast.csv", headers: ["Week (oldest first)", "Closed", "Created"], rows: f.closedWeekly.map((c, i) => [i + 1, c, f.createdWeekly[i]!]) }
    },
  },
  {
    id: "aging", title: "Aging issues", hint: "open longer than 85 % of the issues that closed", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const a = useMemo(() => wipAging(ctx.issues, ctx.now), [ctx.issues, ctx.now])
      if (a.limit === null) return <Empty>Fewer than 5 closed issues on screen: not enough history to say what is slow.</Empty>
      if (a.rows.length === 0) return <Empty>No open issue is older than {Math.round(a.limit)} days.</Empty>
      return (
        <>
          <p className="small muted">Limit: {Math.round(a.limit)} days (what 85 % of closed issues took) · {a.rows.length} of {a.open} open are past it</p>
          <table className="table breakdown">
            <tbody>
              {a.rows.slice(0, 10).map(({ issue, age: d, idle, ratio }) => (
                <tr key={issue.key} onClick={() => ctx.onSelect(issue)}>
                  <td className="wrap title-cell"><Ref item={issue} ctx={ctx} /> {issue.title}</td>
                  <td className="num warn">{Math.round(d)} d · ×{ratio.toFixed(1)}</td>
                  <td className="num muted">idle {Math.round(idle)} d</td>
                </tr>
              ))}
            </tbody>
          </table>
          {a.rows.length > 10 && <p className="small muted">{a.rows.length} in all · <button className="link-btn" onClick={() => ctx.list("Aging issues", (p) => wipAging(p.issues, ctx.now).rows.map((r) => r.issue))}>list all</button></p>}
        </>
      )
    },
    csv: (ctx) => ({ file: "aging.csv", headers: ["Issue", "Title", "Age (days)", "Idle (days)"], rows: wipAging(ctx.issues, ctx.now).rows.map((r) => [r.issue.key, r.issue.title, Math.round(r.age), Math.round(r.idle)]) }),
  },
  {
    id: "data-quality", title: "Data quality", hint: "open issues missing an assignee, milestone, due date or labels", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const q = useMemo(() => dataQuality(ctx.issues), [ctx.issues])
      if (q.open === 0) return <Empty>No open issue on screen.</Empty>
      if (q.incomplete.length === 0) return <Empty>Every open issue has all its fields.</Empty>
      const gaps = (Object.keys(q.byGap) as (keyof typeof q.byGap)[]).filter((g) => g !== "dueDate" || q.usesDue)
      const listIncomplete = (p: Pool) => p.issues.filter((i) => i.state === "OPEN" && gapsOf(i, q.usesDue).length > 0)
      return (
        <>
          <table className="table breakdown">
            <tbody>
              {gaps.map((g) => (
                <tr key={g} onClick={() => ctx.list(`Open issues: ${GAP_LABEL[g].toLowerCase()}`, (p) => p.issues.filter((i) => i.state === "OPEN" && gapsOf(i, q.usesDue).includes(g)))}>
                  <td>{GAP_LABEL[g]}</td>
                  <td className="num">{q.byGap[g]}</td>
                  <td className="num muted">{Math.round((q.byGap[g] / q.open) * 100)} %</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="small muted">{q.incomplete.length} of {q.open} open issues miss something · <button className="link-btn" onClick={() => ctx.list("Open issues with missing fields", listIncomplete)}>list them</button></p>
        </>
      )
    },
    csv: (ctx) => {
      const q = dataQuality(ctx.issues)
      return { file: "data-quality.csv", headers: ["Issue", "Title", "Missing"], rows: q.incomplete.map(({ issue, gaps }) => [issue.key, issue.title, gaps.map((g) => GAP_LABEL[g]).join("; ")]) }
    },
  },
  {
    id: "workflows", title: "Other workflows", hint: "not a PR's CI: Dependabot, Pages, releases and tags", w: S(4), min: S(3), needs: "ci",
    Body: ({ ctx }) => (
      <table className="table breakdown">
        <tbody>
          {Object.entries(ctx.ci?.other ?? {}).map(([kind, v]) => (
            <tr key={kind}>
              <td>{OTHER_LABEL[kind] ?? kind}</td>
              <td className="num">{v.count} runs</td>
              <td>{v.last ? <><span className={`ci-dot ci-${v.last.state}`} /> {v.last.state} · <a href={v.last.url} target="_blank" rel="noreferrer">last run ↗</a></> : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    ),
  },
  {
    id: "labels", title: "Labels", hint: "click a label to filter by it", w: S(4), min: S(3),
    Body: ({ ctx }) => {
      const rows = useMemo(() => labelCounts(ctx.issues).slice(0, 14), [ctx.issues])
      if (rows.length === 0) return <Empty>No issue on screen has labels.</Empty>
      const toggle = (name: string) => ctx.onFilters({ labels: ctx.filters.labels.includes(name) ? ctx.filters.labels.filter((l) => l !== name) : [...ctx.filters.labels, name] })
      return (
        <table className="table breakdown">
          <tbody>
            {rows.map((l) => (
              <tr key={l.name} className={ctx.filters.labels.includes(l.name) ? "picked" : ""} onClick={() => toggle(l.name)}>
                <td className="wrap"><span className="label" style={{ borderColor: `#${l.color}` }}>{l.name}</span></td>
                <td className="num">{l.open} open</td>
                <td className="num muted">{l.closed} closed</td>
              </tr>
            ))}
          </tbody>
        </table>
      )
    },
    csv: (ctx) => ({ file: "labels.csv", headers: ["Label", "Open", "Closed"], rows: labelCounts(ctx.issues).map((l) => [l.name, l.open, l.closed]) }),
  },
]

export const byId = new Map(CATALOG.map((w) => [w.id, w]))

/** The page as it was before the dashboard, widget for widget: the layout a repo starts with. */
export const DEFAULT_IDS = [
  "kpi-open", "kpi-flow30", "kpi-cycle", "kpi-merged30", "kpi-merge", "kpi-noreview",
  "breakdown", "epics", "blockers",
  "open-over-time", "issues-weekly", "pulls-weekly", "close-hist", "merge-hist", "size-merge",
  "cross", "oldest", "overdue", "data-quality", "workflows", "labels",
]

export const PRESETS: { id: string; label: string; ids: string[] }[] = [
  { id: "overview", label: "Overview", ids: ["kpi-open", "kpi-flow30", "kpi-cycle", "kpi-merged30", "kpi-merge", "kpi-wip", "breakdown", "open-over-time", "issues-weekly", "pulls-weekly", "oldest", "overdue", "labels"] },
  { id: "flow", label: "Flow", ids: ["kpi-flow30", "kpi-cycle", "kpi-wip", "kpi-merged30", "kpi-merge", "kpi-open", "cycle-trend", "cfd", "forecast", "aging", "issues-weekly", "pulls-weekly", "wip", "close-hist", "oldest"] },
  { id: "prs", label: "PR quality", ids: ["kpi-merged30", "kpi-merge", "kpi-noreview", "kpi-wip", "merge-hist", "review-hist", "commit-merge-hist", "size-merge", "people", "wip", "cross", "workflows"] },
  { id: "deps", label: "Dependencies", ids: ["kpi-open", "kpi-flow30", "kpi-cycle", "epics", "blockers", "breakdown", "overdue", "oldest"] },
  { id: "trackers", label: "Trackers", ids: ["kpi-open", "kpi-flow30", "kpi-cycle", "breakdown", "epics", "blockers", "cycle-trend", "cfd", "forecast", "aging", "data-quality", "open-over-time", "issues-weekly", "close-hist", "oldest", "overdue", "labels"] },
]
