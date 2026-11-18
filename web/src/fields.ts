// The "Key: value" lines a description keeps for itself ("**Depends on:** F3, #97", "Design: `docs/x.md`. Refs #145")
// become fields of the detail, and what is left is the description proper. The keys are the vocabulary's (vocabulary.json,
// or VOCABULARY_FILE on the server): English and Spanish by default, and the server sends its own with every payload.
import defaults from "../../server/src/vocabulary.json"

export interface Field { key: string; value: string }
export interface Vocabulary { blockedBy: string[]; blocks: string[]; fieldKeys: string[] }

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
const alternation = (phrases: string[]) => {
  const list = [...new Set(phrases.map((p) => p.trim()).filter(Boolean))].sort((a, b) => b.length - a.length)
  return list.length ? `(?:${list.map(escape).join("|")})` : "(?!)"
}

// A key ends where a letter or digit does not: plain \\b misreads "Diseño" and fails after punctuation.
const END = "(?![\\p{L}\\p{N}_])"

function compile(v: Vocabulary) {
  const keys = alternation(v.fieldKeys)
  return {
    plain: new RegExp(`^\\s*(${keys})\\s*:\\s*(.*)$`, "iu"),
    prefix: new RegExp(`^(${keys})${END}\\s*:?\\s*(.*)$`, "iu"),
    split: new RegExp(`\\.\\s+(?=${keys}${END})`, "iu"),
    dependencyKey: new RegExp(`^${alternation(v.blockedBy)}$`, "iu"),
  }
}

let patterns = compile(defaults)

/** The vocabulary of the server that sent the data (its VOCABULARY_FILE, if any). */
export function setVocabulary(v: Vocabulary | undefined) {
  if (v) patterns = compile(v)
}

/** Whether a field key says "this depends on / is blocked by" in the vocabulary's words. */
export function isDependencyKey(key: string): boolean {
  return patterns.dependencyKey.test(key.trim())
}

const BOLD = /^\s*\*\*([^*\n]{2,48}?):?\*\*:?\s*(.*)$/

export function parseFields(body: string): { fields: Field[]; rest: string } {
  const fields: Field[] = []
  const rest: string[] = []
  for (const line of body.split("\n")) {
    const m = BOLD.exec(line) ?? patterns.plain.exec(line)
    if (!m) {
      rest.push(line)
      continue
    }
    const key = m[1]!.trim()
    const [first, ...more] = m[2]!.split(patterns.split)
    fields.push({ key, value: first!.replace(/\.$/, "").trim() })
    for (const extra of more) {
      const e = patterns.prefix.exec(extra)
      if (e) fields.push({ key: e[1]!.trim(), value: e[2]!.replace(/\.$/, "").trim() })
    }
  }
  return { fields: fields.filter((f) => f.value), rest: rest.join("\n").replace(/\n{3,}/g, "\n\n") }
}
