// An item's key is text: "12" on GitHub and GitLab, "PROJ-12" in Jira. These are the helpers for showing and ordering keys.

/** How a key is written in the page: "#12" when it is a number (GitHub, GitLab), the key itself otherwise. */
export const refOf = (key: string) => (/^\d+$/.test(key) ? `#${key}` : key)

/** Numbers sort as numbers ("9" before "10"); other keys as text. */
export function byKey(a: string, b: string): number {
  return /^\d+$/.test(a) && /^\d+$/.test(b) ? Number(a) - Number(b) : a.localeCompare(b)
}
