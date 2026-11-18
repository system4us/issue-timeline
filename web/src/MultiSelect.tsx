import { useEffect, useMemo, useRef, useState } from "react"

const NONE_VALUE = "(none)" // the value that stands for "no milestone" / "no assignee" (see filters.ts NONE)

export interface Option { value: string; label?: string; count?: number; color?: string }

interface Props {
  title: string
  options: Option[]
  selected: string[]
  onChange: (v: string[]) => void
}

/** A dropdown of checkboxes with a search box and a count per option. */
export function MultiSelect({ title, options, selected, onChange }: Props) {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState("")
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => { if (!box.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener("mousedown", close)
    return () => document.removeEventListener("mousedown", close)
  }, [open])

  const shown = useMemo(() => options.filter((o) => (o.label ?? o.value).toLowerCase().includes(q.toLowerCase())), [options, q])
  const toggle = (v: string) => onChange(selected.includes(v) ? selected.filter((x) => x !== v) : [...selected, v])

  return (
    <div className="ms" ref={box} onClick={(e) => e.stopPropagation()}>
      <button title={selected.length > 1 ? selected.join(", ") : undefined} className={selected.length ? "ms-on" : ""} onClick={() => setOpen(!open)} aria-expanded={open}>
        {selected.length === 0 ? title : selected.length === 1 ? `${title}: ${selected[0] === NONE_VALUE ? "(none)" : selected[0]}` : `${title} · ${selected.length}`} ▾
      </button>
      {open && (
        <div className="ms-pop">
          <input autoFocus placeholder={`Search ${title.toLowerCase()}`} value={q} onChange={(e) => setQ(e.target.value)} />
          <div className="ms-list">
            {shown.map((o) => (
              <label key={o.value}>
                <input type="checkbox" checked={selected.includes(o.value)} onChange={() => toggle(o.value)} />
                {o.color && <i className="ms-dot" style={{ background: `#${o.color}` }} />}
                <span className="ms-name">{o.label ?? o.value}</span>
                {o.count !== undefined && <span className="muted">{o.count}</span>}
              </label>
            ))}
            {shown.length === 0 && <p className="muted">No results</p>}
          </div>
          {selected.length > 0 && <button className="ms-clear" onClick={() => onChange([])}>Clear</button>}
        </div>
      )}
    </div>
  )
}
