import { describe, expect, it } from "vitest"
import { EMPTY_FILTERS, NONE, activeChips } from "./filters"

describe("activeChips", () => {
  it("is empty with no filters", () => {
    expect(activeChips(EMPTY_FILTERS)).toEqual([])
  })

  it("gives one chip per value, and each removes only its own value", () => {
    const f = { ...EMPTY_FILTERS, labels: ["bug", "ui"], milestones: [NONE] }
    const chips = activeChips(f)
    expect(chips.map((c) => c.text)).toEqual(["milestone: (none)", "label: bug", "label: ui"])
    const removeBug = chips.find((c) => c.key === "label-bug")!
    expect(removeBug.patch).toEqual({ labels: ["ui"] })
  })

  it("describes the single-choice filters and how to clear them", () => {
    const chips = activeChips({ ...EMPTY_FILTERS, state: "open", linkage: "without", blocking: "blockers", parentsOnly: true, createdFrom: "2026-01-01" })
    expect(chips.map((c) => c.text)).toEqual(["open only", "no link", "with children / as tree", "open blockers", "created from 2026-01-01"])
    expect(chips.find((c) => c.key === "state")!.patch).toEqual({ state: "all" })
  })

  it("leaves out the label-mode choice, which is not a filter of its own", () => {
    expect(activeChips({ ...EMPTY_FILTERS, labelMode: "all" })).toEqual([])
  })
})
