import { describe, expect, it } from "vitest"
import { approvers } from "./reviews"

describe("approvers", () => {
  it("lists who approved, once each", () => {
    expect(approvers([
      { author: "ana", state: "APPROVED", at: "2026-01-02" },
      { author: "bo", state: "COMMENTED", at: "2026-01-03" },
      { author: "ana", state: "APPROVED", at: "2026-01-04" },
    ])).toEqual(["ana"])
  })
  it("a later change request takes the approval back", () => {
    expect(approvers([
      { author: "ana", state: "APPROVED", at: "2026-01-02" },
      { author: "ana", state: "CHANGES_REQUESTED", at: "2026-01-05" },
    ])).toEqual([])
  })
  it("is empty without reviews", () => {
    expect(approvers([])).toEqual([])
  })
})
