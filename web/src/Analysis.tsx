import { useEffect, useMemo, useRef, useState } from "react"
import type { DepEdge, Issue, Item, Link, Pull, TreeEdge } from "./types"
import { NONE, applyFilters, linkIndex, type Filters, type LinkIndex } from "./filters"
import { DAY, blockedNow, blockerRanking, epicRows, breakdown, days as spanDays, type Bin, type GroupBy, weekStart, firstReviewDays, histogram, issueCycleDays, issuesWithAndWithoutPull, labelCounts, median, mergedBetween, openOverTime, oldestOpen, perWeek, pullMergeDays, sizeVsMerge } from "./metrics"
import { Histogram, OpenLine, Scatter, WeeklyBars } from "./charts"
import { downloadCsv } from "./csv"
import type { CiSummary } from "./CiBadge"

const OTHER_LABEL: Record<string, string> = { dependabot: "Dependabot", pages: "Pages deployments", other: "Releases, tags and other workflows" }

const fmt = (v: number | null, unit = "d") => (v === null ? "—" : `${v < 10 ? v.toFixed(1) : Math.round(v)} ${unit}`)

function Card({ title, value, hint, onClick }: { title: string; value: string; hint?: string; onClick?: () => void }) {
  return (
    <div className={`card${onClick ? " clickable" : ""}`} onClick={onClick} title={onClick ? "Click to list them" : undefined}>
      <div className="card-value">{value}</div>
      <div className="card-title">{title}</div>
      {hint && <div className="muted small">{hint}</div>}
    </div>
  )
}

const GROUPS: { value: GroupBy; label: string }[] = [
  { value: "milestone", label: "Milestone" }, { value: "etiqueta", label: "Label" }, { value: "autor", label: "Author" }, { value: "asignado", label: "Assignee" },
]

/** What the analysis is built from: the items on screen (the filters applied) and the links between them. */
interface Pool {
  issues: Issue[]
  pulls: Pull[]
  idx: LinkIndex
  linked: Set<number> // issues with a PR behind them
}

/**
 * A list picked from the analysis (an epic's children, a week of a chart, a histogram bar, a card...), with a link to each item.
 * It keeps how it was picked, not its items: `from` is read again from the current filters, so the list follows them.
 */
interface PickSpec {
  title: string
  from: (p: Pool) => Item[]
  epic?: number // for an epic: the list can also filter everything by it
}

const childrenSorted = (idx: LinkIndex, n: number): Issue[] =>
  [...new Map((idx.childrenOf.get(n) ?? []).map((k) => [k.number, k])).values()]
    .sort((a, b) => Number(a.state === "CLOSED") - Number(b.state === "CLOSED") || a.number - b.number)

const LIST_LIMIT = 200
const round1 = (v: number | null) => (v === null ? null : Math.round(v * 10) / 10)
const dayOf = (t: number) => new Date(t).toISOString().slice(0, 10)
const inWeek = (iso: string | null, w: number) => { const t = iso ? Date.parse(iso) : NaN; return t >= w && t < w + 7 * DAY }
const issueSpan = (i: Item) => (i.kind === "issue" && i.closedAt ? spanDays(i.createdAt, i.closedAt) : null)
const pullSpan = (i: Item) => (i.kind === "pull" && i.mergedAt ? spanDays(i.createdAt, i.mergedAt) : null)

export function Analysis({ issues, pulls, links, tree, deps, all, filters, onFilters, onSelect, ci }: { ci?: CiSummary; issues: Issue[]; pulls: Pull[]; links: Link[]; tree: TreeEdge[]; deps: DepEdge[]; all: { issues: Issue[]; pulls: Pull[] }; filters: Filters; onFilters: (p: Partial<Filters>) => void; onSelect: (i: Item) => void }) {
  const [groupBy, setGroupBy] = useState<GroupBy>("milestone")
  const now = useMemo(() => Date.now(), [])
  // Most of a repo's life can be quiet weeks: the series start at the chosen window, not at the first issue.
  const [windowDays, setWindowDays] = useState(90)
  const from = windowDays > 0 ? now - windowDays * DAY : 0
  const [pickSpec, setPickSpec] = useState<PickSpec | null>(null)
  const pickRef = useRef<HTMLElement>(null)
  // The list appears at the top of the page: bring it into view when a chart further down asked for it.
  useEffect(() => { if (pickSpec) pickRef.current?.scrollIntoView({ behavior: "smooth", block: "nearest" }) }, [pickSpec])

  const m = useMemo(() => {
    const cycle = issueCycleDays(issues)
    const merge = pullMergeDays(pulls)
    const review = firstReviewDays(pulls)
    const last30 = now - 30 * DAY
    return {
      cycle, merge, review,
      open: issues.filter((i) => i.state === "OPEN").length,
      created30: issues.filter((i) => Date.parse(i.createdAt) >= last30).length,
      closed30: issues.filter((i) => i.closedAt && Date.parse(i.closedAt) >= last30).length,
      merged30: mergedBetween(pulls, last30, now).length,
      openPulls: pulls.filter((p) => p.state === "OPEN"),
      flowIssues: perWeek(issues.map((i) => i.createdAt), issues.map((i) => i.closedAt), now).filter((w) => w.week >= weekStart(from)),
      flowPulls: perWeek(pulls.map((p) => p.createdAt), pulls.map((p) => p.mergedAt), now).filter((w) => w.week >= weekStart(from)),
      openLine: openOverTime(issues, now).filter((p) => p.t >= from),
      oldest: oldestOpen(issues, now, 8),
      labels: labelCounts(issues).slice(0, 14),
      cross: issuesWithAndWithoutPull(issues, links),
      scatter: sizeVsMerge(pulls),
      noReview: pulls.filter((p) => p.state === "MERGED" && p.reviewsCount === 0),
    }
  }, [issues, pulls, links, now, from])

  const field = { milestone: "milestones", etiqueta: "labels", autor: "authors", asignado: "assignees" }[groupBy] as "milestones" | "labels" | "authors" | "assignees"
  // The table compares every group, so it ignores the filter of its own dimension (picking a milestone must not hide the others),
  // and the state filter too: a group's progress is its closed share, which an "open only" view would always show as 0%.
  const rows = useMemo(() => {
    const base = applyFilters(all.issues, all.pulls, links, { ...filters, [field]: [], state: "all" }, tree, deps)
    return breakdown(base.issues, links, base.pulls, groupBy, now)
  }, [all, links, tree, deps, filters, field, groupBy, now])
  // Clicking a row narrows every other view to that milestone / label / author / assignee.
  const pick = (key: string) => {
    const cur = filters[field]
    onFilters({ [field]: cur.includes(key) ? cur.filter((k) => k !== key) : [...cur, key] } as Partial<Filters>)
  }
  // The tree and the dependencies of what is on screen (a child or a blocker outside the filter still counts for its epic).
  const idx = useMemo(() => linkIndex(all.issues, all.pulls, links, tree, deps), [all, links, tree, deps])
  const epics = useMemo(() => {
    const shown = new Set(issues.map((i) => i.number))
    return epicRows(idx, now).filter((r) => shown.has(r.epic.number))
  }, [idx, issues, now])
  const blockers = useMemo(() => blockerRanking(idx), [idx])
  const blockedList = useMemo(() => blockedNow(issues, idx), [issues, idx])
  const linked = useMemo(() => new Set(links.map((l) => l.issue)), [links])
  const pool = useMemo<Pool>(() => ({ issues, pulls, idx, linked }), [issues, pulls, idx, linked])
  // The open list is recomputed from the current pool on every render, so it follows the filters while it stays open.
  const picked = useMemo(() => (pickSpec ? { ...pickSpec, items: pickSpec.from(pool) } : null), [pickSpec, pool])
  const cycleBins = histogram(m.cycle, [1, 3, 7, 14, 30, 90])
  const mergeBins = histogram(m.merge, [0.1, 0.5, 1, 3, 7, 14])
  const pickPull = (n: number) => { const p = pulls.find((x) => x.number === n); if (p) onSelect(p) }

  const list = (title: string, from: (p: Pool) => Item[]) => setPickSpec({ title, from })
  const pickEpic = (epic: Issue) =>
    setPickSpec({ title: `Children of #${epic.number} ${epic.title}`, from: (p) => childrenSorted(p.idx, epic.number), epic: epic.number })
  const toggleEpic = (n: number) => {
    const key = String(n)
    onFilters({ epics: filters.epics.includes(key) ? filters.epics.filter((e) => e !== key) : [...filters.epics, key] })
  }
  const toggleLabel = (name: string) => onFilters({ labels: filters.labels.includes(name) ? filters.labels.filter((l) => l !== name) : [...filters.labels, name] })
  const pickWeek = (kind: "issues" | "pulls", w: number) =>
    kind === "issues"
      ? list(`Issues created or closed in the week of ${dayOf(w)}`, (p) => p.issues.filter((i) => inWeek(i.createdAt, w) || inWeek(i.closedAt, w)))
      : list(`PRs opened or merged in the week of ${dayOf(w)}`, (p) => p.pulls.filter((x) => inWeek(x.createdAt, w) || inWeek(x.mergedAt, w)))
  const pickBin = (what: string, kind: "issues" | "pulls", span: (i: Item) => number | null) => (b: Bin) =>
    list(
      `${what}: ${b.to === Infinity ? `${b.from} days or more` : `${b.from} to ${b.to} days`}`,
      (p) => {
        const src: Item[] = kind === "issues" ? p.issues : p.pulls
        return src.filter((i) => { const d = span(i); return d !== null && d >= b.from && d < b.to })
      },
    )
  const pickedOpen = picked ? picked.items.filter((i) => i.state === "OPEN").length : 0
  // The tables as they are, for a spreadsheet (the whole blockers list, not only the first rows on screen).
  const exportBreakdown = () =>
    downloadCsv(`breakdown-${groupBy}.csv`,
      [GROUPS.find((g) => g.value === groupBy)!.label, "Issues", "Closed", "Open", "Progress %", "Median close (d)", "p90 close (d)", "PRs", "Merged PRs", "Median merge (d)", "Open without PR", "Oldest open (d)", "Due", "Overdue"],
      rows.map((r) => [r.key === NONE ? "(none)" : r.key, r.total, r.closed, r.open, Math.round(r.pct * 100), round1(r.medianCycle), round1(r.p90Cycle), r.pulls, r.merged, round1(r.medianMerge), r.withoutPull, r.oldestOpen === null ? null : Math.round(r.oldestOpen), r.due ? r.due.slice(0, 10) : null, r.overdue]))
  const exportEpics = () =>
    downloadCsv("epics.csv",
      ["Epic", "Title", "Children", "Closed", "Open", "Progress %", "With PR", "Open without PR", "Blocked", "Median close (d)", "Oldest open (d)"],
      epics.map((r) => [r.epic.number, r.epic.title, r.total, r.closed, r.open, Math.round(r.pct * 100), r.withPull, r.openWithoutPull, r.blocked, round1(r.medianCycle), r.oldestOpen === null ? null : Math.round(r.oldestOpen)]))
  const exportBlockers = () =>
    downloadCsv("blockers.csv",
      ["Issue", "Title", "Blocks directly", "Blocks in the chain", "Blocked by open issues"],
      blockers.map((b) => [b.issue.number, b.issue.title, b.direct, b.transitive, b.blockedBy]))

  return (
    // Clicks inside the analysis stop here: the app closes the detail panel on any click outside it, which used to
    // clear the item a link had just selected.
    <div className="analysis" onClick={(e) => e.stopPropagation()}>
      <p className="range">
        Series window:{" "}
        {[[30, "30 d"], [90, "90 d"], [180, "6 months"], [365, "1 year"], [0, "All"]].map(([d, l]) => (
          <button key={l} className={windowDays === d ? "on" : ""} onClick={() => setWindowDays(d as number)}>{l}</button>
        ))}
      </p>
      <div className="cards">
        <Card title="Open issues" value={String(m.open)} hint={`of ${issues.length} total · click to list`} onClick={() => list("Open issues", (p) => p.issues.filter((i) => i.state === "OPEN"))} />
        <Card title="Created / closed (30 d)" value={`${m.created30} / ${m.closed30}`} hint={m.created30 > m.closed30 ? "the backlog grows" : "the backlog shrinks or holds"} />
        <Card title="Median time to close an issue" value={fmt(median(m.cycle))} hint={`p90 ${fmt(m.cycle.length ? [...m.cycle].sort((a, b) => a - b)[Math.floor(m.cycle.length * 0.9)]! : null)}`} />
        <Card title="PRs merged (30 d)" value={String(m.merged30)} hint={`${m.openPulls.length} open now`} />
        <Card title="Median time to merge" value={fmt(median(m.merge), "d")} hint={`median to 1st review ${fmt(median(m.review))}`} />
        <Card title="PRs merged without review" value={String(m.noReview.length)} hint={`of ${pulls.filter((p) => p.state === "MERGED").length} merged · click to list`} onClick={() => list("PRs merged without review", (p) => p.pulls.filter((x) => x.state === "MERGED" && x.reviewsCount === 0))} />
      </div>

      {picked && (
        <section className="panel pick-panel" ref={pickRef}>
          <h3>
            {picked.title}
            <span className="muted small"> · {picked.items.length} · {pickedOpen} open</span>
            <span className="pick-actions">
              {picked.epic !== undefined && (
                <button onClick={() => toggleEpic(picked.epic as number)}>
                  {filters.epics.includes(String(picked.epic)) ? "Remove the epic filter" : "Filter everything by this epic"}
                </button>
              )}
              <button onClick={() => setPickSpec(null)} aria-label="Close the list" title="Close the list">×</button>
            </span>
          </h3>
          {picked.items.length === 0 ? (
            <p className="muted">Nothing here.</p>
          ) : (
            <div className="scroll-x">
              <table className="table breakdown">
                <thead><tr><th>State</th><th>Type</th><th>#</th><th>Title</th><th>Labels</th><th>PRs</th></tr></thead>
                <tbody>
                  {picked.items.slice(0, LIST_LIMIT).map((it) => (
                    <tr key={`${it.kind}-${it.number}`} onClick={() => onSelect(it)}>
                      <td><span className={`pill ${it.state.toLowerCase()}`}>{it.state}</span></td>
                      <td>{it.kind === "issue" ? "Issue" : "PR"}</td>
                      <td><button className="link-btn" onClick={(e) => { e.stopPropagation(); onSelect(it) }}>#{it.number}</button></td>
                      <td className="wrap">{it.title}</td>
                      <td>{it.labels.map((l) => <span key={l.name} className="label" style={{ borderColor: `#${l.color}` }}>{l.name}</span>)}</td>
                      <td>{it.kind === "issue" ? idx.pullsOf.get(it.number)?.length ?? 0 : ""}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {picked.items.length > LIST_LIMIT && <p className="muted small">Showing the first {LIST_LIMIT} of {picked.items.length}.</p>}
            </div>
          )}
        </section>
      )}

      <section className="panel">
        <h3>
          How each{" "}
          <select value={groupBy} onChange={(e) => setGroupBy(e.target.value as GroupBy)}>{GROUPS.map((g) => <option key={g.value} value={g.value}>{g.label}</option>)}</select>
          <span className="muted small"> · {rows.length} groups with the other filters applied (not the state: each group shows all its issues); a click on a row filters everything by it</span>
          <button className="csv-btn" onClick={exportBreakdown} title="Download this table as CSV">Export CSV</button>
        </h3>
        <div className="scroll-x">
          <table className="table breakdown">
            <thead>
              <tr><th>{GROUPS.find((g) => g.value === groupBy)!.label}</th><th>Issues</th><th>Progress</th><th>Open</th><th>Median close</th><th>p90</th><th>PRs</th><th>Median merge</th><th>Open without PR</th><th>Oldest</th>{groupBy === "milestone" && <th>Due</th>}</tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key} className={filters[field].includes(r.key) ? "picked" : ""} onClick={() => pick(r.key)}>
                  <td>{r.key === NONE ? <i className="muted">(none)</i> : r.key}</td>
                  <td>{r.total}</td>
                  <td className="progress-cell"><div className="progress"><div style={{ width: `${r.pct * 100}%` }} /></div><span>{Math.round(r.pct * 100)}%</span></td>
                  <td>{r.open}</td>
                  <td>{fmt(r.medianCycle)}</td>
                  <td>{fmt(r.p90Cycle)}</td>
                  <td>{r.merged}/{r.pulls}</td>
                  <td>{fmt(r.medianMerge)}</td>
                  <td className={r.withoutPull && r.open === r.withoutPull ? "warn" : ""}>{r.withoutPull}</td>
                  <td>{r.oldestOpen === null ? "—" : `${Math.round(r.oldestOpen)} d`}</td>
                  {groupBy === "milestone" && <td className={r.overdue ? "error" : ""}>{r.due ? r.due.slice(0, 10) : "—"}{r.overdue ? " · overdue" : ""}</td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      {(epics.length > 0 || blockers.length > 0) && (
        <div className="grid two">
          {epics.length > 0 && (
            <section>
              <h3>Epics and issues with children <span className="muted small">· click a row to list its children · progress counts the real state of each child</span><button className="csv-btn" onClick={exportEpics} title="Download this table as CSV">Export CSV</button></h3>
              <div className="scroll-x">
                <table className="table breakdown">
                  <thead><tr><th>Epic</th><th>Children</th><th>Progress</th><th>Open</th><th>With PR</th><th>Without PR</th><th>Blocked</th><th>Median close</th><th>Oldest</th></tr></thead>
                  <tbody>
                    {epics.map((r) => (
                      <tr key={r.epic.number} className={picked?.epic === r.epic.number ? "picked" : ""} onClick={() => pickEpic(r.epic)}>
                        <td className="wrap"><button className="link-btn" onClick={(e) => { e.stopPropagation(); onSelect(r.epic) }}>#{r.epic.number}</button> {r.epic.title}</td>
                        <td>{r.total}</td>
                        <td className="progress-cell"><div className="progress"><div style={{ width: `${r.pct * 100}%` }} /></div><span>{r.closed}/{r.total}</span></td>
                        <td>{r.open}</td>
                        <td>{r.withPull}</td>
                        <td className={r.openWithoutPull ? "warn" : ""}>{r.openWithoutPull}</td>
                        <td className={r.blocked ? "error" : ""}>{r.blocked}</td>
                        <td>{fmt(r.medianCycle)}</td>
                        <td>{r.oldestOpen === null ? "—" : `${Math.round(r.oldestOpen)} d`}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          )}
          <section>
            <h3>Blockers <span className="muted small">· {blockedList.length} issues blocked now by something open</span><button className="csv-btn" onClick={exportBlockers} title="Download all blockers as CSV">Export CSV</button></h3>
            {blockers.length === 0 ? (
              <p className="muted">No open issue blocks another open one.</p>
            ) : (
              <div className="scroll-x">
                <table className="table breakdown">
                  <thead><tr><th>Open blocker</th><th>Blocks</th><th>Chained</th><th>Itself blocked by</th></tr></thead>
                  <tbody>
                    {blockers.slice(0, 12).map((b) => (
                      <tr key={b.issue.number} onClick={() => onSelect(b.issue)}>
                        <td className="wrap"><b>#{b.issue.number}</b> {b.issue.title}</td>
                        <td>{b.direct}</td>
                        <td className={b.transitive > b.direct ? "warn" : ""}>{b.transitive}</td>
                        <td className={b.blockedBy ? "error" : ""}>{b.blockedBy}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {blockedList.length > 0 && (
              <p className="small muted">
                Blocked now: {blockedList.slice(0, 14).map((i, k) => <span key={i.number}>{k ? ", " : ""}<button className="link-btn" onClick={() => onSelect(i)}>#{i.number}</button></span>)}{blockedList.length > 14 ? "…" : ""}
                {" "}<button className="link-btn" onClick={() => list("Blocked now", (p) => blockedNow(p.issues, p.idx))}>list all {blockedList.length}</button>
              </p>
            )}
          </section>
        </div>
      )}

      <div className="grid">
        <section>
          <h3>Open issues over time</h3>
          <OpenLine points={m.openLine} />
        </section>
        <section>
          <h3>Issues created and closed per week <span className="muted small">· click a week to list it</span></h3>
          <WeeklyBars weeks={m.flowIssues} aLabel="created" bLabel="closed" onPick={(w) => pickWeek("issues", w)} />
        </section>
        <section>
          <h3>PRs opened and merged per week <span className="muted small">· click a week to list it</span></h3>
          <WeeklyBars weeks={m.flowPulls} aLabel="opened" bLabel="merged" onPick={(w) => pickWeek("pulls", w)} />
        </section>
        <section>
          <h3>Days to close an issue <span className="muted small">· click a bar to list those issues</span></h3>
          <Histogram bins={cycleBins} unit="days" onPick={pickBin("Issues closed in", "issues", issueSpan)} />
        </section>
        <section>
          <h3>Days to merge a PR <span className="muted small">· click a bar to list those PRs</span></h3>
          <Histogram bins={mergeBins} unit="days" onPick={pickBin("PRs merged in", "pulls", pullSpan)} />
        </section>
        <section>
          <h3>PR size vs merge time</h3>
          <Scatter points={m.scatter} onPick={pickPull} />
        </section>

        <section>
          <h3>Issues ↔ PRs cross-check <span className="muted small">· click a row to list it</span></h3>
          <table className="table breakdown">
            <tbody>
              <tr onClick={() => list("Closed issues with a PR behind", (p) => p.issues.filter((i) => i.closedAt && p.linked.has(i.number)))}><td>Closed issues with a PR behind</td><td>{m.cross.withPull.count}</td><td>median {fmt(m.cross.withPull.median)}</td></tr>
              <tr onClick={() => list("Closed issues without a PR", (p) => p.issues.filter((i) => i.closedAt && !p.linked.has(i.number)))}><td>Closed issues without a PR</td><td>{m.cross.withoutPull.count}</td><td>median {fmt(m.cross.withoutPull.median)}</td></tr>
              <tr onClick={() => list("Open issues with no PR", (p) => p.issues.filter((i) => i.state === "OPEN" && !p.linked.has(i.number)))}><td>Open issues with no PR</td><td>{m.cross.openWithoutPull}</td><td /></tr>
            </tbody>
          </table>
        </section>

        <section>
          <h3>Oldest open issues</h3>
          <table className="table breakdown">
            <tbody>
              {m.oldest.map(({ issue, age }) => (
                <tr key={issue.number} onClick={() => onSelect(issue)}>
                  <td><button className="link-btn" onClick={(e) => { e.stopPropagation(); onSelect(issue) }}>#{issue.number}</button></td>
                  <td className="wrap">{issue.title}</td>
                  <td>{Math.round(age)} d</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>

        {ci && Object.keys(ci.other).length > 0 && (
          <section>
            <h3>Other workflows <span className="muted small">· not a PR's CI: Dependabot, Pages, releases and tags</span></h3>
            <table className="table breakdown">
              <tbody>
                {Object.entries(ci.other).map(([kind, v]) => (
                  <tr key={kind}>
                    <td>{OTHER_LABEL[kind] ?? kind}</td>
                    <td>{v.count} runs</td>
                    <td>{v.last ? <><span className={`ci-dot ci-${v.last.state}`} /> {v.last.state} · <a href={v.last.url} target="_blank" rel="noreferrer">last run ↗</a></> : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
        )}

        <section>
          <h3>Labels <span className="muted small">· click a label to filter by it</span></h3>
          <table className="table breakdown">
            <tbody>
              {m.labels.map((l) => (
                <tr key={l.name} className={filters.labels.includes(l.name) ? "picked" : ""} onClick={() => toggleLabel(l.name)}>
                  <td><span className="label" style={{ borderColor: `#${l.color}` }}>{l.name}</span></td>
                  <td>{l.open} open</td>
                  <td>{l.closed} closed</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      </div>
    </div>
  )
}
