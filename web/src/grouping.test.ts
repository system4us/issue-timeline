import { describe, expect, it } from "vitest"
import { groupOf } from "./grouping"
import { linkIndex, NONE } from "./filters"
import type { Issue, Pull } from "./types"

const issue = (n: number, assignees: string[]): Issue => ({
  kind: "issue", id: `i${n}`, key: String(n), number: n, title: `t${n}`, url: "", author: "a", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z",
  closedAt: null, labels: [], assignees, milestone: null, commentsCount: 0, labelEvents: 0, state: "OPEN", stateReason: null,
})

describe("grouping by assignee", () => {
  const a = issue(1, ["ana", "luis"])
  const b = issue(2, [])
  const pr = { kind: "pull", id: "p1", key: "10", number: 10, title: "pr", url: "", author: "x", createdAt: "2026-11-02T00:00:00Z", updatedAt: "2026-11-02T00:00:00Z", closedAt: null, labels: [], assignees: [], milestone: null, commentsCount: 0, labelEvents: 0, state: "OPEN", isDraft: false, mergedAt: null, mergedBy: null, reviewDecision: null, base: "main", head: "f", additions: 0, deletions: 0, changedFiles: 0, closes: [], commitsCount: 1, firstReviewAt: null, reviewsCount: 0 } as Pull
  const idx = linkIndex([a, b], [pr], [{ pull: "10", issue: "1" }] as never, [], [])
  it("puts an issue under its first assignee, or the one the filter asked for, and unassigned ones apart", () => {
    expect(groupOf(a, "asignado", idx)).toBe("ana")
    expect(groupOf(a, "asignado", idx, ["luis"])).toBe("luis")
    expect(groupOf(b, "asignado", idx)).toBe(NONE)
  })
  it("a pull request follows the assignees of its issues", () => {
    expect(groupOf(pr, "asignado", idx)).toBe("ana")
  })
})
