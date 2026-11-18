/** Who approved: each reviewer's latest review counts, so a later "changes requested" or comment replaces an approval. */
export function approvers(reviews: { author: string | null; state: string; at: string | null }[]): string[] {
  const latest = new Map<string, string>()
  for (const r of [...reviews].sort((a, b) => (a.at ?? "").localeCompare(b.at ?? ""))) latest.set(r.author ?? "", r.state)
  return [...latest].filter(([, state]) => state === "APPROVED").map(([author]) => author).filter(Boolean)
}
