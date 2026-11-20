// A tracker's custom fields as the page shows them. The value is what the tracker gives: a string, a list for a field with
// several values, or null. The format is the tracker's field type when the key may read the definitions (Redmine admins),
// and null otherwise: then the value is shown as it is.

export interface CustomField {
  id: number
  name: string
  format: string | null
  multiple: boolean
  value: string | string[] | null
}

/** The text of a field's value for the page. */
export function fieldText(f: CustomField): string {
  const v = f.value
  if (v === null || v === undefined || v === "" || (Array.isArray(v) && v.length === 0)) return "—"
  if (Array.isArray(v)) return v.join(", ")
  if (f.format === "bool") return v === "1" ? "yes" : v === "0" ? "no" : v
  if (f.format === "user") return `user #${v}` // Redmine gives the user's id; the name needs the user list
  return v
}

/** A link is shown as one when its value is a web address. */
export const isLink = (f: CustomField) => f.format === "link" && typeof f.value === "string" && /^https?:\/\//.test(f.value)
