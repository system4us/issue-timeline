import type { Issue, Item } from "./types"
import { NONE, assigneesOf, labelsOf, milestonesOf, type LinkIndex } from "./filters"
import { byKey, refOf } from "./keys"

export type TimelineGroup = "ninguno" | "milestone" | "etiqueta" | "autor" | "epica"

/** The row group of an item (one per item: an item with several labels sits under its first). */
export function groupOf(item: Item, by: TimelineGroup, idx: LinkIndex, prefer: string[] = []): string {
  // A pull request that follows several issues goes under the group the filter asked for, if it is one of its own.
  const pick = (xs: string[]) => xs.find((x) => prefer.includes(x)) ?? xs[0] ?? NONE
  switch (by) {
    case "milestone": return pick(milestonesOf(item, idx))
    case "etiqueta": return pick(labelsOf(item, idx))
    case "autor": return item.author ?? NONE
    case "epica": {
      // under its parent (an issue that has children is the head of its own group); a pull request, under the group of
      // its issues, the epic the filter asked for first if it is one of them
      const issues = item.kind === "issue" ? [item] : idx.issuesOf.get(item.key) ?? []
      const heads: Issue[] = []
      for (const i of issues) {
        const parent = idx.parentsOf.get(i.key)?.[0] ?? ((idx.childrenOf.get(i.key)?.length ?? 0) > 0 ? i : null)
        if (parent) heads.push(...(idx.parentsOf.get(i.key)?.length ? idx.parentsOf.get(i.key)! : [parent]))
      }
      const head = heads.find((h) => prefer.includes(String(h.key))) ?? heads[0]
      return head ? `${refOf(head.key)} ${head.title}` : NONE
    }
    case "ninguno": return ""
  }
}

export const assigneeOf = (item: Item, idx: LinkIndex) => assigneesOf(item, idx)[0] ?? NONE

/**
 * The rows of a tree of issues, in reading order: each parent, then its children by the time they were opened, the
 * children of a child right under it. An issue that sits under several parents appears once, under the first.
 * `parentOf` maps a child to the parent it was placed under (for the connectors).
 */
export function treeOrder(issues: Issue[], idx: LinkIndex): { order: Issue[]; parentOf: Map<string, string> } {
  const inSet = new Map(issues.map((i) => [i.key, i]))
  const byTime = (a: Issue, b: Issue) => Date.parse(a.createdAt) - Date.parse(b.createdAt) || byKey(a.key, b.key)
  const hasParentHere = (i: Issue) => (idx.parentsOf.get(i.key) ?? []).some((p) => inSet.has(p.key))
  const roots = issues.filter((i) => !hasParentHere(i)).sort((a, b) => Number(!idx.childrenOf.has(a.key)) - Number(!idx.childrenOf.has(b.key)) || byTime(a, b))
  const order: Issue[] = []
  const parentOf = new Map<string, string>()
  const seen = new Set<string>()
  const visit = (i: Issue, parent: string | null) => {
    if (seen.has(i.key)) return
    seen.add(i.key)
    order.push(i)
    if (parent !== null) parentOf.set(i.key, parent)
    for (const k of (idx.childrenOf.get(i.key) ?? []).filter((k) => inSet.has(k.key)).sort(byTime)) visit(inSet.get(k.key)!, i.key)
  }
  for (const r of roots) visit(r, null)
  for (const i of issues.sort(byTime)) visit(i, null) // anything left (a cycle) still gets a row
  return { order, parentOf }
}
