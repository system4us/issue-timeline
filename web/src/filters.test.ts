import { describe, expect, it } from "vitest"
import { EMPTY_FILTERS, NONE, activeChips, applyFilters, fromQuery, isEmpty, linkIndex, toQuery, type Filters } from "./filters"
import { blockedNow, blockerRanking, breakdown, epicRows } from "./metrics"
import { treeOrder } from "./grouping"
import type { Issue, Pull } from "./types"

const issue = (n: number, o: Partial<Issue> = {}): Issue => ({
  kind: "issue", id: `i${n}`, key: String(n), number: n, title: `t${n}`, url: "", author: "ana", createdAt: "2026-01-01T00:00:00Z", updatedAt: "", closedAt: null,
  labels: [], assignees: [], milestone: null, commentsCount: 0, labelEvents: 0, state: "OPEN", stateReason: null, ...o,
})
const pull = (n: number, o: Partial<Pull> = {}): Pull => ({
  kind: "pull", id: `p${n}`, key: String(n), number: n, title: `pr${n}`, url: "", author: "ana", createdAt: "2026-01-02T00:00:00Z", updatedAt: "", closedAt: "2026-01-03T00:00:00Z",
  labels: [], assignees: [], milestone: null, commentsCount: 0, labelEvents: 0, state: "MERGED", isDraft: false, mergedAt: "2026-01-03T00:00:00Z", mergedBy: "ana",
  reviewDecision: null, base: "main", head: "x", additions: 1, deletions: 1, changedFiles: 1, closes: [], commitsCount: 1, firstReviewAt: null, reviewsCount: 0, ...o,
})
const m = (title: string, dueOn: string | null = null) => ({ title, dueOn })
const l = (name: string) => ({ name, color: "fff" })

const issues = [
  issue(1, { milestone: m("M1"), labels: [l("bug"), l("backend")] }),
  issue(2, { milestone: m("M1"), labels: [l("backend")], state: "CLOSED", closedAt: "2026-01-11T00:00:00Z" }),
  issue(3, { milestone: m("M2", "2026-01-15T00:00:00Z"), labels: [l("bug")] }),
  issue(4),
]
const pulls = [pull(10), pull(11)]
const links = [{ issue: "2", pull: "10", via: "closes" as const }, { issue: "3", pull: "11", via: "closes" as const }]
const run = (f: Partial<Filters>) => {
  const r = applyFilters(issues, pulls, links, { ...EMPTY_FILTERS, ...f })
  return { issues: r.issues.map((i) => i.key), pulls: r.pulls.map((p) => p.key) }
}

describe("filters", () => {
  it("filters by milestone, and a pull request follows the milestone of its issues", () => {
    expect(run({ milestones: ["M1"] })).toEqual({ issues: ["1", "2"], pulls: ["10"] })
    expect(run({ milestones: ["M2"] })).toEqual({ issues: ["3"], pulls: ["11"] })
  })

  it("'sin milestone' keeps what has none", () => {
    expect(run({ milestones: [NONE] })).toEqual({ issues: ["4"], pulls: [] })
  })

  it("labels match any or all of the chosen ones", () => {
    expect(run({ labels: ["bug", "backend"] }).issues).toEqual(["1", "2", "3"])
    expect(run({ labels: ["bug", "backend"], labelMode: "all" }).issues).toEqual(["1"])
  })

  it("combines milestone, label and state", () => {
    expect(run({ milestones: ["M1"], labels: ["backend"], state: "open" }).issues).toEqual(["1"])
  })

  it("filters by whether there is a pull request behind", () => {
    expect(run({ linkage: "without" }).issues).toEqual(["1", "4"])
    expect(run({ linkage: "with" }).issues).toEqual(["2", "3"])
  })

  it("filters by creation date", () => {
    expect(run({ createdFrom: "2026-01-02" }).issues).toEqual([])
  })

  it("round-trips through the URL", () => {
    const f: Filters = { ...EMPTY_FILTERS, milestones: ["M1", "M 2"], labels: ["bug"], labelMode: "all", state: "open", query: "x y" }
    expect(fromQuery(toQuery(f))).toEqual(f)
  })
})

describe("breakdown", () => {
  it("summarizes each milestone: progress, cycle time, pull requests, overdue", () => {
    const rows = breakdown(issues, links, pulls, "milestone", Date.parse("2026-02-01T00:00:00Z"))
    const m1 = rows.find((r) => r.key === "M1")!
    expect(m1).toMatchObject({ total: 2, open: 1, closed: 1, pct: 0.5, medianCycle: 10, pulls: 1, merged: 1, withoutPull: 1 })
    const m2 = rows.find((r) => r.key === "M2")!
    expect(m2.overdue).toBe(true)
    expect(rows.find((r) => r.key === NONE)!.total).toBe(1)
  })

  it("an issue with two labels counts in both", () => {
    const rows = breakdown(issues, links, pulls, "etiqueta", Date.parse("2026-02-01T00:00:00Z"))
    expect(rows.find((r) => r.key === "backend")!.total).toBe(2)
    expect(rows.find((r) => r.key === "bug")!.total).toBe(2)
  })
})

describe("hierarchy and dependencies", () => {
  const iss = [1, 2, 3, 4, 5].map((n) => issue(n, { state: n === 2 ? "CLOSED" : "OPEN", closedAt: n === 2 ? "2026-01-11T00:00:00Z" : null }))
  const tree = [{ parent: "1", child: "2", via: "checklist" as const, checked: true }, { parent: "1", child: "3", via: "checklist" as const, checked: false }, { parent: "3", child: "4", via: "native" as const, checked: null }]
  const deps = [{ blocker: "5", blocked: "3", via: "text" as const }, { blocker: "3", blocked: "4", via: "text" as const }, { blocker: "2", blocked: "5", via: "text" as const }]
  const run2 = (f: Partial<Filters>) => applyFilters(iss, [], [], { ...EMPTY_FILTERS, ...f }, tree, deps).issues.map((i) => i.key)

  it("an epic filter keeps the parent and everything under it", () => {
    expect(run2({ epics: ["1"] })).toEqual(["1", "2", "3", "4"])
    expect(run2({ epics: ["3"] })).toEqual(["3", "4"])
  })

  it("only issues with children, or in a tree", () => {
    expect(run2({ parentsOnly: true })).toEqual(["1", "2", "3", "4"])
  })

  it("blocked means held by an open issue; a closed blocker holds nothing", () => {
    expect(run2({ blocking: "blocked" })).toEqual(["3", "4"])
    expect(run2({ blocking: "blockers" })).toEqual(["3", "5"])
  })

  it("ranks the blockers by what they hold back, all the way down", () => {
    const idx = linkIndex(iss, [], [], tree, deps)
    const rows = blockerRanking(idx)
    expect(rows.map((r) => [r.issue.key, r.direct, r.transitive])).toEqual([["5", 1, 2], ["3", 1, 1]])
    expect(blockedNow(iss, idx).map((i) => i.key)).toEqual(["3", "4"])
  })

  it("summarizes each epic by the real state of its children", () => {
    const idx = linkIndex(iss, [], [], tree, deps)
    const rows = epicRows(idx, Date.parse("2026-02-01T00:00:00Z"))
    const e1 = rows.find((r) => r.epic.key === "1")!
    expect(e1).toMatchObject({ total: 2, closed: 1, open: 1, pct: 0.5, blocked: 1, depth: 2 })
  })
})

describe("treeOrder", () => {
  it("puts the parent first, then the children by the time they were opened, a child's children right under it", () => {
    const mk = (n: number, createdAt: string) => issue(n, { createdAt })
    const list = [mk(1, "2026-01-01T00:00:00Z"), mk(2, "2026-01-05T00:00:00Z"), mk(3, "2026-01-03T00:00:00Z"), mk(4, "2026-01-06T00:00:00Z"), mk(5, "2026-01-04T00:00:00Z")]
    const tree = [
      { parent: "1", child: "2", via: "checklist" as const, checked: false },
      { parent: "1", child: "3", via: "checklist" as const, checked: false },
      { parent: "3", child: "5", via: "checklist" as const, checked: false },
      { parent: "2", child: "4", via: "checklist" as const, checked: false },
    ]
    const idx = linkIndex(list, [], [], tree)
    const { order, parentOf } = treeOrder(list, idx)
    expect(order.map((i) => i.key)).toEqual(["1", "3", "5", "2", "4"])
    expect(parentOf.get("5")).toBe("3")
    expect(parentOf.get("1")).toBeUndefined()
  })
})

describe("the overdue filter", () => {
  const dueIssue = (n: number, dueDate: string | null, state: "OPEN" | "CLOSED" = "OPEN") => issue(n, { dueDate, state })
  const list = [dueIssue(1, "2000-01-01"), dueIssue(2, "2099-01-01"), dueIssue(3, "2000-01-01", "CLOSED"), dueIssue(4, null)]
  it("keeps only open issues whose due day has passed", () => {
    const r = applyFilters(list, [], [], { ...EMPTY_FILTERS, overdue: true })
    expect(r.issues.map((i) => i.key)).toEqual(["1"])
  })
  it("the flag is in the URL and comes back from it, and has a chip to remove it", () => {
    const f = { ...EMPTY_FILTERS, overdue: true }
    expect(toQuery(f)).toContain("vencidos=1")
    expect(fromQuery(toQuery(f)).overdue).toBe(true)
    expect(activeChips(f).map((c) => c.text)).toContain("overdue only")
    expect(isEmpty(f)).toBe(false)
  })
})
