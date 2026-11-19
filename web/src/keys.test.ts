import { describe, expect, it } from "vitest"
import { byKey, refOf } from "./keys"

describe("keys", () => {
  it("writes a numeric key as #N and any other key as it is", () => {
    expect(refOf("12")).toBe("#12")
    expect(refOf("PROJ-12")).toBe("PROJ-12")
  })

  it("sorts numbers as numbers and the rest as text", () => {
    expect(["10", "9"].sort(byKey)).toEqual(["9", "10"])
    expect(["PROJ-2", "PROJ-10"].sort(byKey)).toEqual(["PROJ-10", "PROJ-2"])
  })
})
