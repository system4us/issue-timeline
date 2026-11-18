import { useEffect, useRef, useState, type ReactNode, type RefObject } from "react"
import type { Saved } from "./session"

// The in-place editing primitives: a value that turns into its control where it is shown. Every control commits on its
// own (Enter, blur, a choice, the switch's click, Save) and cancels with Escape. A failed save puts the old value back
// and says why under the field, with Retry.

const errorText = (e: unknown) => String((e as Error)?.message ?? e)

export interface FieldEdit {
  editing: boolean
  busy: boolean
  error: string | null
  warnings: string[]
  trigger: RefObject<HTMLButtonElement>
  begin: () => void
  /** Leaves the control without saving. `refocus` puts the focus back on the value (a key did it; a click elsewhere does not). */
  cancel: (refocus?: boolean) => void
  /** Saves `next`; the caller has checked that it changes something. Resolves true when the host kept it. */
  run: (next: unknown, refocus?: boolean) => Promise<boolean>
  retry: () => void
  dismiss: () => void
}

export function useFieldEdit(save: (next: any) => Promise<Saved | undefined> | undefined): FieldEdit {
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [warnings, setWarnings] = useState<string[]>([])
  const [failed, setFailed] = useState<{ next: unknown } | null>(null)
  const [focusTick, setFocusTick] = useState(0)
  const trigger = useRef<HTMLButtonElement>(null)
  const saveRef = useRef(save)
  saveRef.current = save

  // The focus goes back to the value once its control has gone (not while the save runs: the value is disabled then).
  useEffect(() => { if (focusTick > 0) trigger.current?.focus() }, [focusTick])

  const run = async (next: unknown, refocus = false): Promise<boolean> => {
    setEditing(false)
    setBusy(true)
    setError(null)
    setWarnings([])
    setFailed(null)
    try {
      const out = await saveRef.current(next)
      setWarnings(out?.warnings ?? [])
      return true
    } catch (e) {
      setError(errorText(e))
      setFailed({ next })
      return false
    } finally {
      setBusy(false)
      if (refocus) setFocusTick((n) => n + 1)
    }
  }
  const cancel = (refocus = true) => {
    setEditing(false)
    if (refocus) setFocusTick((n) => n + 1)
  }
  const begin = () => { setError(null); setEditing(true) }
  const retry = () => { if (failed) void run(failed.next, true) }
  const dismiss = () => { setError(null); setFailed(null) }
  return { editing, busy, error, warnings, trigger, begin, cancel, run, retry, dismiss }
}

/** The error (with Retry) and the host's warnings, right under the field. */
export function FieldMessages({ edit }: { edit: FieldEdit }) {
  return (
    <>
      {edit.error && (
        <span role="alert" className="inline-error">
          {edit.error}{" "}
          <button type="button" onClick={edit.retry}>Retry</button>{" "}
          <button type="button" className="link-btn" onClick={edit.dismiss}>Dismiss</button>
        </span>
      )}
      {edit.warnings.map((w) => <span key={w} className="inline-warning">{w}</span>)}
    </>
  )
}

export function Pencil() {
  return (
    <svg className="inline-pencil" viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden="true">
      <path d="M11 2.5l2.5 2.5L6 12.5H3.5V10z" />
    </svg>
  )
}

/** The value as a button (with a pencil on hover and focus). Read-only, the value is shown as it always was. */
export function Field({ edit, enabled, label, valueText, display, control, popover = false }: {
  edit: FieldEdit; enabled: boolean; label: string; valueText: string; display: ReactNode; control: ReactNode; popover?: boolean
}) {
  if (!enabled) return <>{display}</>
  const trigger = (
    <button
      ref={edit.trigger}
      type="button"
      className={`inline-trigger${popover ? " is-popover" : ""}`}
      disabled={edit.busy}
      onClick={edit.begin}
      aria-label={`${label}: ${valueText}`}
      aria-haspopup={popover ? "dialog" : undefined}
      aria-expanded={popover ? edit.editing : undefined}
      title={`Edit ${label.toLowerCase()}`}
    >
      {display}
      {edit.busy ? <span className="inline-spinner" role="status" aria-label={`Saving ${label}`} /> : <Pencil />}
    </button>
  )
  if (popover) {
    return (
      <span className="inline-field">
        {trigger}
        {edit.editing && control}
        <FieldMessages edit={edit} />
      </span>
    )
  }
  if (edit.editing) return <span className="inline-field is-editing">{control}<FieldMessages edit={edit} /></span>
  return <span className="inline-field">{trigger}<FieldMessages edit={edit} /></span>
}

/** A single line (text, a date, a number): Enter or leaving the box commits, Escape cancels. */
export function TextControl({ label, initial, type = "text", placeholder, onCommit, onCancel }: {
  label: string; initial: string; type?: string; placeholder?: string
  onCommit: (draft: string, refocus: boolean) => void
  onCancel: (refocus: boolean) => void
}) {
  const [draft, setDraft] = useState(initial)
  const done = useRef(false)
  const finish = (fn: () => void) => { if (!done.current) { done.current = true; fn() } }
  return (
    <input
      aria-label={label}
      autoFocus
      type={type}
      className="inline-input"
      value={draft}
      placeholder={placeholder}
      onChange={(e) => setDraft(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter") { e.preventDefault(); finish(() => onCommit(draft, true)) }
        else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(() => onCancel(true)) }
      }}
      onBlur={() => finish(() => onCommit(draft, false))}
    />
  )
}

/** A text or a description: Save and Cancel under it (Ctrl or Cmd with Enter saves, Escape cancels). */
export function AreaControl({ label, initial, rows = 6, onSave, onCancel }: {
  label: string; initial: string; rows?: number
  onSave: (draft: string, refocus: boolean) => void
  onCancel: (refocus: boolean) => void
}) {
  const [draft, setDraft] = useState(initial)
  return (
    <span className="inline-area">
      <textarea
        aria-label={label}
        autoFocus
        rows={rows}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); onCancel(true) }
          else if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); onSave(draft, true) }
        }}
      />
      <span className="inline-actions">
        <button type="button" onClick={() => onCancel(true)}>Cancel</button>
        <button type="button" className="primary" onClick={() => onSave(draft, true)}>Save</button>
      </span>
    </span>
  )
}

export interface ListOption { key: string; label: string; value: unknown; color?: string }
/** The option of a value: its key is the text the list compares (null and "" are the empty choice). */
export const keyOf = (v: unknown) => (v === null || v === undefined ? "" : String(v))
export const opt = (value: unknown, label: string): ListOption => ({ key: keyOf(value), label, value })

/**
 * A popover under the value: a filter and a list. One choice commits at once; a checklist commits when it is closed
 * (Done, or a click elsewhere). Escape closes it without a change.
 */
export function ListControl({ label, options, selected, multi, onDone, onCancel }: {
  label: string; options: ListOption[] | null; selected: string[]; multi: boolean
  onDone: (keys: string[], refocus: boolean) => void
  onCancel: (refocus: boolean) => void
}) {
  const [draft, setDraft] = useState(selected)
  // The choices that were selected when it opened come first (a checklist keeps its order while it is ticked).
  const [initiallySelected] = useState(selected)
  const [filter, setFilter] = useState("")
  const box = useRef<HTMLSpanElement>(null)
  const settled = useRef(false)
  const latest = useRef({ draft, onDone, onCancel, multi })
  latest.current = { draft, onDone, onCancel, multi }

  // A click outside closes it: a checklist keeps what was ticked, a single choice keeps what it had.
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (settled.current || !box.current || box.current.contains(e.target as Node)) return
      settled.current = true
      if (latest.current.multi) latest.current.onDone(latest.current.draft, false)
      else latest.current.onCancel(false)
    }
    document.addEventListener("mousedown", onDown)
    return () => document.removeEventListener("mousedown", onDown)
  }, [])

  const all = options ?? []
  // A filter only when there are many choices: a list of three needs none (and takes the focus itself, so Escape works).
  const filterable = all.length > 8
  useEffect(() => { if (!filterable) box.current?.focus() }, [])
  const matching = all.filter((o) => o.label.toLowerCase().includes(filter.trim().toLowerCase()))
  const shown = multi
    ? [...matching.filter((o) => initiallySelected.includes(o.key)), ...matching.filter((o) => !initiallySelected.includes(o.key))]
    : matching
  const finish = (fn: () => void) => { if (!settled.current) { settled.current = true; fn() } }
  const toggle = (key: string) => setDraft((d) => (d.includes(key) ? d.filter((x) => x !== key) : [...d, key]))

  return (
    <span className="inline-pop" role="dialog" aria-label={label} ref={box} tabIndex={-1}
      onKeyDown={(e) => {
        if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(() => onCancel(true)) }
      }}>
      {filterable && (
        <input
          autoFocus
          className="inline-input"
          aria-label={`Filter ${label}`}
          placeholder="Filter…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !multi && shown[0]) { e.preventDefault(); finish(() => onDone([shown[0].key], true)) }
            if (e.key === "Enter" && multi) { e.preventDefault(); finish(() => onDone(draft, true)) }
          }}
        />
      )}
      {options === null ? <span className="muted small">Loading…</span> : shown.length === 0 ? <span className="muted small">Nothing to choose from.</span> : (
        <span className="inline-options" role={multi ? "group" : "listbox"} aria-label={label}>
          {shown.map((o) => {
            const on = multi ? draft.includes(o.key) : selected.includes(o.key)
            return multi ? (
              <button key={o.key} type="button" role="checkbox" aria-checked={on} className={`inline-option${on ? " on" : ""}`} onClick={() => toggle(o.key)}>
                <span className={`inline-box${on ? " on" : ""}`} aria-hidden="true">{on ? "✓" : ""}</span>
                {o.color && <span className="inline-swatch" style={{ background: `#${o.color}` }} aria-hidden="true" />}
                <span>{o.label}</span>
              </button>
            ) : (
              <button key={o.key} type="button" role="option" aria-selected={on} className={`inline-option${on ? " on" : ""}`} onClick={() => finish(() => onDone([o.key], true))}>
                {o.color && <span className="inline-swatch" style={{ background: `#${o.color}` }} aria-hidden="true" />}
                <span>{o.label}</span>
              </button>
            )
          })}
        </span>
      )}
      {multi && (
        <span className="inline-actions">
          <button type="button" onClick={() => finish(() => onCancel(true))}>Cancel</button>
          <button type="button" className="primary" onClick={() => finish(() => onDone(draft, true))}>Done</button>
        </span>
      )}
    </span>
  )
}

/** A switch: its click saves at once. */
export function SwitchField({ label, checked, edit, onToggle }: { label: string; checked: boolean; edit: FieldEdit; onToggle: () => void }) {
  return (
    <span className="inline-field">
      <button ref={edit.trigger} type="button" role="switch" aria-checked={checked} aria-label={label} disabled={edit.busy} className={`inline-switch${checked ? " on" : ""}`} onClick={onToggle}>
        <span className="inline-switch-knob" aria-hidden="true">&nbsp;</span>
        <span className="inline-switch-text">{checked ? "Yes" : "No"}</span>
        {edit.busy && <span className="inline-spinner" role="status" aria-label={`Saving ${label}`} />}
      </button>
      <FieldMessages edit={edit} />
    </span>
  )
}

/** A choice that is not a record's field (the create panel): the same popover, with its value kept by the caller. */
export function Picker({ label, options, selected, multi = false, onChange, text }: {
  label: string; options: ListOption[] | null; selected: string[]; multi?: boolean
  onChange: (keys: string[]) => void; text: string
}) {
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  const close = (refocus: boolean) => {
    setOpen(false)
    if (refocus) trigger.current?.focus()
  }
  return (
    <span className="inline-field new-picker">
      <button ref={trigger} type="button" className="inline-trigger is-popover" aria-label={`${label}: ${text}`} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <span className="inline-picker-text">{text}</span>
        <Pencil />
      </button>
      {open && (
        <ListControl
          label={label}
          options={options}
          selected={selected}
          multi={multi}
          onDone={(keys, refocus) => { onChange(keys); close(refocus) }}
          onCancel={(r) => close(r)}
        />
      )}
    </span>
  )
}
