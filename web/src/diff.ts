// Side-by-side rows from a unified patch. The removed and the added lines of one change sit on the same row when they
// pair up, so an edited line reads as one change, not as a block removed above a block added.

export interface Cell {
  n: number | null // line number on its side (null for the empty side of a pure addition or removal)
  text: string
  kind: "ctx" | "del" | "add" | "empty"
}

export type SplitRow = { type: "hunk"; text: string } | { type: "line"; left: Cell; right: Cell }

const EMPTY: Cell = { n: null, text: "", kind: "empty" }
const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/

export function splitPatch(patch: string): SplitRow[] {
  const rows: SplitRow[] = []
  let oldN = 0
  let newN = 0
  let dels: Cell[] = []
  let adds: Cell[] = []

  // The removed and added lines gathered since the last context line become rows, side by side.
  const flush = () => {
    const count = Math.max(dels.length, adds.length)
    for (let i = 0; i < count; i++) rows.push({ type: "line", left: dels[i] ?? EMPTY, right: adds[i] ?? EMPTY })
    dels = []
    adds = []
  }

  for (const line of patch.split("\n")) {
    const hunk = HUNK.exec(line)
    if (hunk) {
      flush()
      oldN = Number(hunk[1])
      newN = Number(hunk[2])
      rows.push({ type: "hunk", text: line })
    } else if (line.startsWith("-")) {
      dels.push({ n: oldN++, text: line.slice(1), kind: "del" })
    } else if (line.startsWith("+")) {
      adds.push({ n: newN++, text: line.slice(1), kind: "add" })
    } else if (line.startsWith(" ")) {
      flush()
      const text = line.slice(1)
      rows.push({ type: "line", left: { n: oldN++, text, kind: "ctx" }, right: { n: newN++, text, kind: "ctx" } })
    }
    // Anything else ("\ No newline at end of file", the empty tail after the last newline) carries no code.
  }
  flush()
  return rows
}

/** The lines added and removed by a patch, for the stats of a file. */
export function patchStats(patch: string): { additions: number; deletions: number } {
  let additions = 0
  let deletions = 0
  for (const line of patch.split("\n")) {
    if (line.startsWith("+")) additions++
    else if (line.startsWith("-")) deletions++
  }
  return { additions, deletions }
}
