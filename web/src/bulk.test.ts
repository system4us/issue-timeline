import { describe, expect, it } from "vitest"
import { bulkBody, bulkKinds, runBulk, STOP_AFTER, type BulkOp } from "./bulk"
import type { Issue, Pull } from "./types"

const gh = { host: "github", pull: false } as const
const gl = { host: "gitlab", pull: false } as const
const rm = { host: "redmine", pull: false } as const

const issue = (key: string, extra: Partial<Issue> = {}): Issue => ({
  kind: "issue", id: `i${key}`, key, number: Number(key), title: `t${key}`, url: "", author: "a", createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z",
  closedAt: null, labels: [], assignees: [], milestone: null, commentsCount: 0, labelEvents: 0, state: "OPEN", stateReason: null, ...extra,
})

describe("bulkKinds", () => {
  it("offers labels to GitHub and GitLab, due dates to GitLab and Redmine", () => {
    expect(bulkKinds(gh)).not.toContain("dueDate")
    expect(bulkKinds(gl)).toContain("dueDate")
    expect(bulkKinds(gl)).toContain("addLabel")
    expect(bulkKinds(rm)).not.toContain("addLabel")
    expect(bulkKinds(rm)).toContain("dueDate")
  })
})

describe("bulkBody", () => {
  it("assigns: a list on GitHub/GitLab, an id on Redmine, and skips what already is", () => {
    const op: BulkOp = { kind: "assign", id: 7, name: "ana" }
    expect(bulkBody(gl, op, issue("1"))).toEqual({ assignees: [7] })
    expect(bulkBody(rm, op, issue("1"))).toEqual({ assignee: 7 })
    expect(bulkBody(gl, op, issue("1", { assignees: ["ana"] }))).toBeNull()
    expect(bulkBody(gl, op, issue("1", { assignees: ["ana", "luis"] }))).toEqual({ assignees: [7] }) // replaces: it is "assign to"
    expect(bulkBody(gl, { kind: "assign", id: null, name: null }, issue("1", { assignees: ["ana"] }))).toEqual({ assignees: [] })
    expect(bulkBody(gl, { kind: "assign", id: null, name: null }, issue("1"))).toBeNull()
  })
  it("sets the milestone (the version on Redmine) and clears it", () => {
    const op: BulkOp = { kind: "milestone", id: 3, name: "v1" }
    expect(bulkBody(gh, op, issue("1"))).toEqual({ milestone: 3 })
    expect(bulkBody(rm, op, issue("1"))).toEqual({ version: 3 })
    expect(bulkBody(gh, op, issue("1", { milestone: { title: "v1", dueOn: null } }))).toBeNull()
    expect(bulkBody(gh, { kind: "milestone", id: null, name: null }, issue("1", { milestone: { title: "v1", dueOn: null } }))).toEqual({ milestone: null })
  })
  it("adds and removes a label keeping the others", () => {
    const labelled = issue("1", { labels: [{ name: "bug", color: "f00" }] })
    expect(bulkBody(gh, { kind: "addLabel", name: "p1" }, labelled)).toEqual({ labels: ["bug", "p1"] })
    expect(bulkBody(gh, { kind: "addLabel", name: "bug" }, labelled)).toBeNull()
    expect(bulkBody(gh, { kind: "removeLabel", name: "bug" }, labelled)).toEqual({ labels: [] })
    expect(bulkBody(gh, { kind: "removeLabel", name: "nope" }, labelled)).toBeNull()
  })
  it("sets the state and the due date, skipping what already is", () => {
    expect(bulkBody(gl, { kind: "state", to: "CLOSED" }, issue("1"))).toEqual({ state: "CLOSED" })
    expect(bulkBody(gl, { kind: "state", to: "OPEN" }, issue("1"))).toBeNull()
    expect(bulkBody(gl, { kind: "dueDate", date: "2026-12-01" }, issue("1"))).toEqual({ dueDate: "2026-12-01" })
    expect(bulkBody(gl, { kind: "dueDate", date: "2026-12-01" }, issue("1", { dueDate: "2026-12-01T00:00:00Z" }))).toBeNull()
    expect(bulkBody(gl, { kind: "dueDate", date: null }, issue("1", { dueDate: "2026-12-01" }))).toEqual({ dueDate: null })
  })
  it("never sends a pull request", () => {
    const pull = { ...issue("5"), kind: "pull" } as unknown as Pull
    expect(bulkBody(gh, { kind: "state", to: "CLOSED" }, pull)).toBeNull()
  })
})

describe("runBulk", () => {
  const op: BulkOp = { kind: "state", to: "CLOSED" }
  it("sends one PATCH per issue that changes, never more than `concurrency` at once, and reports each", async () => {
    const items = [issue("1"), issue("2"), issue("3", { state: "CLOSED" }), issue("4"), issue("5")]
    let live = 0
    let peak = 0
    const sent: string[] = []
    const progress: number[] = []
    const r = await runBulk(items, gl, op, async (key) => {
      live++; peak = Math.max(peak, live)
      await new Promise((res) => setTimeout(res, 5))
      live--; sent.push(key)
    }, { concurrency: 2, onProgress: (n) => progress.push(n) })
    expect(r.changed.sort()).toEqual(["1", "2", "4", "5"])
    expect(r.skipped).toEqual(["3"])
    expect(r.failed).toEqual([])
    expect(peak).toBeLessThanOrEqual(2)
    expect(sent.sort()).toEqual(["1", "2", "4", "5"])
    expect(progress.at(-1)).toBe(4)
  })
  it("keeps going past a single failure and says why", async () => {
    const r = await runBulk([issue("1"), issue("2"), issue("3")], gl, op, async (key) => { if (key === "2") throw new Error("403 forbidden") }, { concurrency: 1 })
    expect(r.changed).toEqual(["1", "3"])
    expect(r.failed).toEqual([{ key: "2", error: "403 forbidden" }])
    expect(r.notTried).toEqual([])
  })
  it(`stops after ${STOP_AFTER} failures in a row and reports the rest as not tried`, async () => {
    const items = Array.from({ length: 10 }, (_, i) => issue(String(i + 1)))
    let sent = 0
    const r = await runBulk(items, gl, op, async () => { sent++; throw new Error("401 refused") }, { concurrency: 1 })
    expect(sent).toBe(STOP_AFTER)
    expect(r.failed).toHaveLength(STOP_AFTER)
    expect(r.notTried).toHaveLength(10 - STOP_AFTER)
    expect(r.changed).toEqual([])
  })
})
