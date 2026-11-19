import { describe, expect, it } from "vitest"
import { patchStats, splitPatch } from "./diff"

describe("splitPatch", () => {
  it("puts an edited line on one row, both sides numbered", () => {
    const rows = splitPatch(["@@ -1,3 +1,3 @@", " a", "-b", "+B", " c", ""].join("\n"))
    expect(rows).toEqual([
      { type: "hunk", text: "@@ -1,3 +1,3 @@" },
      { type: "line", left: { n: 1, text: "a", kind: "ctx" }, right: { n: 1, text: "a", kind: "ctx" } },
      { type: "line", left: { n: 2, text: "b", kind: "del" }, right: { n: 2, text: "B", kind: "add" } },
      { type: "line", left: { n: 3, text: "c", kind: "ctx" }, right: { n: 3, text: "c", kind: "ctx" } },
    ])
  })

  it("leaves the other side empty for a pure removal or addition", () => {
    const rows = splitPatch(["@@ -5,2 +5,3 @@", " x", "-gone", "+new1", "+new2"].join("\n"))
    const lines = rows.filter((r) => r.type === "line")
    expect(lines[1]).toEqual({ type: "line", left: { n: 6, text: "gone", kind: "del" }, right: { n: 6, text: "new1", kind: "add" } })
    expect(lines[2]).toEqual({ type: "line", left: { n: null, text: "", kind: "empty" }, right: { n: 7, text: "new2", kind: "add" } })
  })

  it("pairs a block of removals with the block of additions that replaces it", () => {
    const rows = splitPatch(["@@ -1,2 +1,1 @@", "-one", "-two", "+uno"].join("\n"))
    const lines = rows.filter((r) => r.type === "line")
    expect(lines).toHaveLength(2)
    expect(lines[0]).toMatchObject({ left: { text: "one" }, right: { text: "uno" } })
    expect(lines[1]).toMatchObject({ left: { text: "two", kind: "del" }, right: { kind: "empty" } })
  })

  it("restarts the numbering at each hunk and skips the no-newline marker", () => {
    const patch = ["@@ -1 +1 @@", "-a", "\\ No newline at end of file", "+a2", "@@ -40,1 +41,1 @@", " z"].join("\n")
    const rows = splitPatch(patch)
    expect(rows.map((r) => r.type)).toEqual(["hunk", "line", "hunk", "line"])
    expect(rows[3]).toMatchObject({ left: { n: 40 }, right: { n: 41 } })
  })

  it("is empty for an empty patch", () => {
    expect(splitPatch("")).toEqual([])
  })
})

describe("patchStats", () => {
  it("counts added and removed lines", () => {
    expect(patchStats(["@@ -1,2 +1,2 @@", "-a", "+b", "+c", " d"].join("\n"))).toEqual({ additions: 2, deletions: 1 })
  })
})
