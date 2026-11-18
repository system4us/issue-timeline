import { describe, expect, it } from "vitest"
import { fieldText, isLink, type CustomField } from "./customFields"

const field = (o: Partial<CustomField>): CustomField => ({ id: 1, name: "f", format: null, multiple: false, value: null, ...o })

describe("custom fields", () => {
  it("shows each value by its format: yes/no for a boolean, the list joined, the user's id, and a dash when empty", () => {
    expect(fieldText(field({ format: "bool", value: "1" }))).toBe("yes")
    expect(fieldText(field({ format: "bool", value: "0" }))).toBe("no")
    expect(fieldText(field({ format: "list", multiple: true, value: ["seguridad", "ux"] }))).toBe("seguridad, ux")
    expect(fieldText(field({ format: "user", value: "1" }))).toBe("user #1")
    expect(fieldText(field({ format: "list", multiple: true, value: [] }))).toBe("—")
    expect(fieldText(field({ format: "string", value: null }))).toBe("—")
    expect(fieldText(field({ format: null, value: "as is" }))).toBe("as is")
  })

  it("a link is a link only when it is a web address", () => {
    expect(isLink(field({ format: "link", value: "https://example.com" }))).toBe(true)
    expect(isLink(field({ format: "link", value: "javascript:alert(1)" }))).toBe(false)
    expect(isLink(field({ format: "string", value: "https://example.com" }))).toBe(false)
  })
})
