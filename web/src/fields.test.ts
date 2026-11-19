import { describe, expect, it } from "vitest"
import { isDependencyKey, parseFields, setVocabulary } from "./fields"
import defaults from "../../internal/derive/vocabulary.json"

describe("parseFields", () => {
  it("takes bold and plain 'Key: value' lines out of the description, one or two to a line", () => {
    const body = "- Message layers.\n\n**Depends on:** F3, #106, #107.\n\nDesign: `docs/shop.md`. Refs #145.\n\n**Code conventions:** everything in English."
    const { fields, rest } = parseFields(body)
    expect(fields).toEqual([
      { key: "Depends on", value: "F3, #106, #107" },
      { key: "Design", value: "`docs/shop.md`" },
      { key: "Refs", value: "#145" },
      { key: "Code conventions", value: "everything in English" },
    ])
    expect(rest.trim()).toBe("- Message layers.")
  })

  it("reads the same keys written in Spanish, as issues already written in Spanish use them", () => {
    const body = "- Capas de mensajes.\n\n**Depende de:** F3, #106, #107.\n\nDiseño: `docs/tienda.md`. Refs #145."
    const { fields, rest } = parseFields(body)
    expect(fields).toEqual([
      { key: "Depende de", value: "F3, #106, #107" },
      { key: "Diseño", value: "`docs/tienda.md`" },
      { key: "Refs", value: "#145" },
    ])
    expect(rest.trim()).toBe("- Capas de mensajes.")
  })

  it("leaves a body without fields untouched", () => {
    const { fields, rest } = parseFields("Just text\n- [ ] #1 task")
    expect(fields).toEqual([])
    expect(rest).toBe("Just text\n- [ ] #1 task")
  })

  it("reads the keys of the other languages in the default vocabulary", () => {
    const body = "Dépend de: #3.\n\nReferências: `doc.md`. Refs #9."
    const { fields } = parseFields(body)
    expect(fields).toEqual([
      { key: "Dépend de", value: "#3" },
      { key: "Referências", value: "`doc.md`" },
      { key: "Refs", value: "#9" },
    ])
    expect(isDependencyKey("Bloqueado pelo")).toBe(true)
  })

  it("uses the vocabulary the server sends, and only that", () => {
    try {
      setVocabulary({ blockedBy: ["espera a"], blocks: [], fieldKeys: ["Ticket"] })
      expect(parseFields("Ticket: ABC-1\n**Depends on:** #2").fields).toEqual([
        { key: "Ticket", value: "ABC-1" },
        { key: "Depends on", value: "#2" },
      ])
      expect(isDependencyKey("Espera a")).toBe(true)
      expect(isDependencyKey("Depends on")).toBe(false)
    } finally {
      setVocabulary(defaults)
    }
  })
})
