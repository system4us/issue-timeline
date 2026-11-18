// CSV for a spreadsheet: quoted when needed, a BOM first so Excel reads the accents right.

type Cell = string | number | boolean | null | undefined

const cell = (v: Cell): string => {
  if (v === null || v === undefined) return ""
  const text = typeof v === "boolean" ? (v ? "yes" : "no") : String(v)
  return /[",\n\r]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text
}

export function toCsv(headers: string[], rows: Cell[][]): string {
  return [headers, ...rows].map((r) => r.map(cell).join(",")).join("\r\n") + "\r\n"
}

/** Saves the table as a .csv file (the browser's download, no server involved). */
export function downloadCsv(filename: string, headers: string[], rows: Cell[][]): void {
  const blob = new Blob(["﻿" + toCsv(headers, rows)], { type: "text/csv;charset=utf-8" })
  const url = URL.createObjectURL(blob)
  const link = document.createElement("a")
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  link.remove()
  URL.revokeObjectURL(url)
}
