import { describe, expect, it } from "vitest"
import { toCsv } from "./csv"

describe("toCsv", () => {
  it("writes headers, then rows, with CRLF endings", () => {
    expect(toCsv(["a", "b"], [[1, "x"], [2, "y"]])).toBe("a,b\r\n1,x\r\n2,y\r\n")
  })
  it("quotes commas, quotes and line breaks, doubling quotes", () => {
    expect(toCsv(["t"], [['say "hi", ok\nnext']])).toBe('t\r\n"say ""hi"", ok\nnext"\r\n')
  })
  it("leaves nulls empty and writes booleans as yes / no", () => {
    expect(toCsv(["a", "b", "c"], [[null, undefined, true]])).toBe("a,b,c\r\n,,yes\r\n")
    expect(toCsv(["a"], [[false]])).toBe("a\r\nno\r\n")
  })
  it("keeps decimals with a dot", () => {
    expect(toCsv(["median"], [[2.5]])).toBe("median\r\n2.5\r\n")
  })
})
