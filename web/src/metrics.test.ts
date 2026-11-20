import { describe, expect, it } from "vitest"
import { commitToMergeDays, cycleTrend, dataQuality, forecast, wipAging, histogram, isOverdue, overdueIssues, issuesWithAndWithoutPull, median, mergedWithoutReview, openOverTime, peopleLoad, percentile, perWeek, summary, weekStart, workInProgress, DAY } from "./metrics"
import { linkIndex } from "./filters"
import type { Pull } from "./types"
import { packLanes } from "./lanes"
import type { Issue } from "./types"

const issue = (n: number, createdAt: string, closedAt: string | null): Issue => ({
  kind: "issue", id: `i${n}`, key: String(n), number: n, title: `t${n}`, url: "", author: "a", createdAt, updatedAt: createdAt, closedAt, labels: [], assignees: [],
  milestone: null, commentsCount: 0, labelEvents: 0, state: closedAt ? "CLOSED" : "OPEN", stateReason: null,
})

describe("metrics", () => {
  it("percentiles interpolate and handle empty input", () => {
    expect(median([])).toBeNull()
    expect(median([1, 3, 2])).toBe(2)
    expect(percentile([0, 10], 0.25)).toBe(2.5)
  })

  it("weeks start on Monday (UTC)", () => {
    expect(new Date(weekStart(Date.parse("2026-11-12T10:00:00Z"))).toISOString()).toBe("2026-11-09T00:00:00.000Z")
  })

  it("perWeek counts both series over every week, empty ones included", () => {
    const w = perWeek(["2026-11-02T00:00:00Z", "2026-11-03T00:00:00Z"], ["2026-11-17T00:00:00Z"], Date.parse("2026-11-18T00:00:00Z"))
    expect(w.map((p) => [p.a, p.b])).toEqual([[2, 0], [0, 0], [0, 1]])
  })

  it("open over time is created minus closed so far", () => {
    const issues = [issue(1, "2026-11-02T00:00:00Z", "2026-11-12T00:00:00Z"), issue(2, "2026-11-03T00:00:00Z", null)]
    const pts = openOverTime(issues, Date.parse("2026-11-20T00:00:00Z"))
    expect(pts[0]!.open).toBe(2)
    expect(pts.at(-1)!.open).toBe(1)
  })

  it("histogram has an open-ended last bin", () => {
    const h = histogram([0.5, 1.5, 9, 40], [1, 7, 30])
    expect(h.map((b) => b.count)).toEqual([1, 1, 1, 1])
  })

  it("compares issues that have a pull request with the ones that do not", () => {
    const issues = [issue(1, "2026-01-01T00:00:00Z", "2026-01-03T00:00:00Z"), issue(2, "2026-01-01T00:00:00Z", "2026-01-11T00:00:00Z"), issue(3, "2026-01-01T00:00:00Z", null)]
    const r = issuesWithAndWithoutPull(issues, [{ issue: "1", pull: "9", via: "closes" }])
    expect(r.withPull).toEqual({ count: 1, median: 2 })
    expect(r.withoutPull).toEqual({ count: 1, median: 10 })
    expect(r.openWithoutPull).toBe(1)
    expect(DAY).toBe(86_400_000)
  })
})

describe("packLanes", () => {
  it("reuses a lane once the previous span ended, and opens a new one on overlap", () => {
    const { lane, lanes } = packLanes([
      { id: "a", start: 0, end: 10 },
      { id: "b", start: 5, end: 20 },
      { id: "c", start: 12, end: 30 },
      { id: "d", start: 25, end: 26 },
    ], 2)
    expect(lanes).toBe(2)
    expect(lane.get("a")).toBe(0)
    expect(lane.get("b")).toBe(1)
    expect(lane.get("c")).toBe(0)
    expect(lane.get("d")).toBe(1)
  })
})

describe("isOverdue", () => {
  const now = Date.parse("2027-02-10T12:00:00Z")
  it("an open issue whose due day has passed is overdue; a closed one, a PR, or no date is not", () => {
    expect(isOverdue({ kind: "issue", state: "OPEN", dueDate: "2027-02-09" }, now)).toBe(true)
    expect(isOverdue({ kind: "issue", state: "OPEN", dueDate: "2027-02-10" }, now)).toBe(false)
    expect(isOverdue({ kind: "issue", state: "CLOSED", dueDate: "2027-02-01" }, now)).toBe(false)
    expect(isOverdue({ kind: "pull", state: "OPEN", dueDate: "2027-02-01" }, now)).toBe(false)
    expect(isOverdue({ kind: "issue", state: "OPEN", dueDate: null }, now)).toBe(false)
  })
})

describe("overdueIssues", () => {
  it("lists the open overdue issues, most days late first", () => {
    const now = Date.parse("2027-02-10T12:00:00Z")
    const mk = (key: string, state: "OPEN" | "CLOSED", dueDate: string | null) => ({ kind: "issue", key, state, dueDate } as unknown as Issue)
    const list = overdueIssues([mk("A", "OPEN", "2027-02-01"), mk("B", "OPEN", "2027-02-08"), mk("C", "CLOSED", "2027-01-01"), mk("D", "OPEN", null), mk("E", "OPEN", "2027-02-20")], now)
    expect(list.map((r) => [r.issue.key, r.late])).toEqual([["A", 9], ["B", 2]])
  })
})

const pull = (n: number, o: Partial<Pull> = {}): Pull => ({
  kind: "pull", id: `p${n}`, key: String(n), number: n, title: `p${n}`, url: "", author: "ana", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z",
  closedAt: null, labels: [], assignees: [], milestone: null, commentsCount: 0, labelEvents: 0, state: "OPEN", isDraft: false, mergedAt: null, mergedBy: null,
  reviewDecision: null, base: "main", head: `b${n}`, additions: 10, deletions: 2, changedFiles: 1, closes: [], commitsCount: 1, firstReviewAt: null, reviewsCount: 0, ...o,
})
const merged = (n: number, createdAt: string, mergedAt: string, o: Partial<Pull> = {}) => pull(n, { state: "MERGED", createdAt, mergedAt, closedAt: mergedAt, ...o })

describe("dashboard metrics", () => {
  const now = Date.parse("2026-11-18T12:00:00Z")

  it("open over time also carries the closed so far, and ends at now", () => {
    const issues = [issue(1, "2026-11-02T00:00:00Z", "2026-11-12T00:00:00Z"), issue(2, "2026-11-03T00:00:00Z", null), issue(3, "2026-11-17T00:00:00Z", null)]
    const pts = openOverTime(issues, now)
    expect(pts.map((p) => [p.open, p.closed])).toEqual([[2, 0], [1, 1], [2, 1]])
    expect(pts.at(-1)!.t).toBe(now)
  })

  it("the summary holds the headline numbers, the p90 among them", () => {
    const issues = Array.from({ length: 10 }, (_, k) => issue(k + 1, "2026-11-01T00:00:00Z", new Date(Date.parse("2026-11-01T00:00:00Z") + (k + 1) * DAY).toISOString()))
    issues.push(issue(11, "2026-11-10T00:00:00Z", null))
    const pulls = [merged(1, "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z"), merged(2, "2026-11-01T00:00:00Z", "2026-11-05T00:00:00Z", { reviewsCount: 1, firstReviewAt: "2026-11-01T12:00:00Z" }), pull(3)]
    const s = summary(issues, pulls, now)
    expect(s).toMatchObject({ total: 11, open: 1, created30: 11, closed30: 10, cycleMedian: 5.5, merged30: 2, openPulls: 1, mergeMedian: 2.5, reviewMedian: 0.5, mergedTotal: 2, mergedNoReview: 1 })
    expect(s.cycleP90).toBeCloseTo(9.1)
    expect(mergedWithoutReview(pulls).map((p) => p.key)).toEqual(["1"])
  })

  it("cycle trend gives throughput and the median per week of closing, empty weeks included", () => {
    const t = cycleTrend([issue(1, "2026-11-01T00:00:00Z", "2026-11-03T00:00:00Z"), issue(2, "2026-11-01T00:00:00Z", "2026-11-05T00:00:00Z"), issue(3, "2026-11-10T00:00:00Z", "2026-11-17T00:00:00Z")], now)
    expect(t.map((p) => [p.closed, p.median])).toEqual([[2, 3], [0, null], [1, 7]])
    expect(cycleTrend([issue(1, "2026-11-01T00:00:00Z", null)], now)).toEqual([])
  })

  it("first commit to merge uses the oldest commit, which can predate the PR", () => {
    const p = merged(1, "2026-11-05T00:00:00Z", "2026-11-06T00:00:00Z", { commits: [{ oid: "b", message: "", at: "2026-11-04T00:00:00Z", author: null }, { oid: "a", message: "", at: "2026-11-02T00:00:00Z", author: null }] })
    expect(commitToMergeDays([p, merged(2, "2026-11-05T00:00:00Z", "2026-11-06T00:00:00Z"), pull(3)])).toEqual([4])
  })

  it("work in progress: open PRs by age, drafts, and the issues with an open PR behind", () => {
    const issues = [issue(1, "2026-11-01T00:00:00Z", null), issue(2, "2026-11-01T00:00:00Z", null)]
    const pulls = [pull(10, { createdAt: "2026-11-15T12:00:00Z" }), pull(11, { createdAt: "2026-11-08T12:00:00Z", isDraft: true }), merged(12, "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z")]
    const idx = linkIndex(issues, pulls, [{ issue: "1", pull: "10", via: "closes" }, { issue: "2", pull: "12", via: "closes" }])
    const w = workInProgress(issues, pulls, idx, now)
    expect(w.pulls.map((r) => [r.item.key, r.age])).toEqual([["11", 10], ["10", 3]])
    expect(w.drafts).toBe(1)
    expect(w.issues.map((r) => r.item.key)).toEqual(["1"])
    expect(w.medianAge).toBe(6.5)
  })

  it("people load counts what each one opened, merged, and merged for others", () => {
    const rows = peopleLoad([
      merged(1, "2026-11-01T00:00:00Z", "2026-11-03T00:00:00Z", { author: "ana", mergedBy: "bea" }),
      merged(2, "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z", { author: "ana", mergedBy: "ana" }),
      pull(3, { author: "bea" }),
    ])
    expect(rows).toEqual([
      { person: "ana", opened: 2, open: 0, merged: 2, mergedOthers: 0, medianMerge: 1.5 },
      { person: "bea", opened: 1, open: 1, merged: 0, mergedOthers: 1, medianMerge: null },
    ])
  })
})

describe("dataQuality", () => {
  const open = (n: number, extra: Partial<Issue> = {}): Issue => ({ ...issue(n, "2026-11-01T00:00:00Z", null), ...extra })
  it("counts the gaps of open issues only and ignores due dates when the repo has none", () => {
    const q = dataQuality([open(1), open(2, { assignees: ["a"], milestone: { title: "m", dueOn: null }, labels: [{ name: "x", color: "000" }] }), issue(3, "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z")])
    expect(q.open).toBe(2)
    expect(q.usesDue).toBe(false)
    expect(q.byGap).toEqual({ assignee: 1, milestone: 1, dueDate: 0, labels: 1 })
    expect(q.incomplete.map((r) => r.issue.key)).toEqual(["1"])
  })
  it("asks for a due date once any issue has one, and lists the emptiest first", () => {
    const q = dataQuality([open(1, { assignees: ["a"], milestone: { title: "m", dueOn: null }, labels: [{ name: "x", color: "000" }] }), open(2), open(3, { dueDate: "2026-12-01" })])
    expect(q.usesDue).toBe(true)
    expect(q.byGap.dueDate).toBe(2)
    expect(q.incomplete.map((r) => r.issue.key)).toEqual(["2", "3", "1"])
  })
})

describe("forecast and wipAging", () => {
  const now = Date.parse("2026-11-18T12:00:00Z") // Wednesday
  const mk = (n: number, created: string, closed: string | null, updated = created): Issue => ({ ...issue(n, created, closed), updatedAt: updated })
  it("projects the open issues at the pace of the last full weeks", () => {
    // full weeks before 2026-11-16: 2 closed in the week of Nov 2, 4 in the week of Nov 9 (others zero)
    const closed = ["2026-11-03T00:00:00Z", "2026-11-04T00:00:00Z", "2026-11-09T00:00:00Z", "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z", "2026-11-12T00:00:00Z"]
    const issues = [...closed.map((c, i) => mk(i + 1, "2026-10-01T00:00:00Z", c)), mk(10, "2026-10-01T00:00:00Z", null), mk(11, "2026-10-01T00:00:00Z", null)]
    const f = forecast(issues, now, 8)
    expect(f.remaining).toBe(2)
    expect(f.closedWeekly).toEqual([0, 0, 0, 0, 0, 0, 2, 4])
    expect(f.weeks.fast).toBeCloseTo(4) // p75 of [0,0,0,0,0,0,2,4] is 0.5 a week: 2 open / 0.5
    expect(f.weeks.usual).toBeNull() // the usual week closed nothing
    expect(f.growing).toBe(false)
  })
  it("says nothing without a pace, and flags a growing backlog", () => {
    const f = forecast([mk(1, "2026-11-02T00:00:00Z", null), mk(2, "2026-11-03T00:00:00Z", null), mk(3, "2026-11-04T00:00:00Z", null)], now, 4)
    expect(f.dates.usual).toBeNull()
    expect(f.growing).toBe(false) // median created per week is 0 over 4 weeks
    const g = forecast([mk(1, "2026-11-10T00:00:00Z", "2026-11-11T00:00:00Z"), ...[2, 3, 4, 5].map((n) => mk(n, "2026-11-10T00:00:00Z", null))], now, 1)
    expect(g.growing).toBe(true)
  })
  it("flags open issues older than the p85 of what closed took, and needs history", () => {
    const done = [1, 2, 3, 4, 5].map((n) => mk(n, "2026-11-01T00:00:00Z", `2026-11-0${n + 1}T00:00:00Z`)) // cycles of 1 to 5 days
    const a = wipAging([...done, mk(6, "2026-11-10T00:00:00Z", null, "2026-11-17T00:00:00Z"), mk(7, "2026-11-17T00:00:00Z", null)], now)
    expect(a.limit).toBeCloseTo(4.4)
    expect(a.rows.map((r) => r.issue.key)).toEqual(["6"])
    expect(a.rows[0]!.idle).toBeCloseTo(1.5)
    expect(wipAging(done.slice(0, 3), now).limit).toBeNull()
  })
})
