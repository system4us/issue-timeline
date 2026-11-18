import { describe, expect, it } from "vitest"
import { histogram, issuesWithAndWithoutPull, median, openOverTime, percentile, perWeek, weekStart, DAY } from "./metrics"
import { packLanes } from "./lanes"
import type { Issue } from "./types"

const issue = (n: number, createdAt: string, closedAt: string | null): Issue => ({
  kind: "issue", id: `i${n}`, number: n, title: `t${n}`, url: "", author: "a", createdAt, updatedAt: createdAt, closedAt, labels: [], assignees: [],
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
    const r = issuesWithAndWithoutPull(issues, [{ issue: 1, pull: 9, via: "closes" }])
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
