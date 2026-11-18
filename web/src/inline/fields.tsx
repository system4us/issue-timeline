import type { ReactNode } from "react"
import { fieldChange, fieldValue, type Target } from "./changes"
import type { EditSession } from "./session"
import { AreaControl, Field, ListControl, keyOf, SwitchField, TextControl, useFieldEdit, type ListOption } from "./field"

// One field of the item, bound to the record: the value as the record has it, and a control that saves only that
// field (one PATCH, built by fieldChange). A change that changes nothing closes the control and sends nothing.

interface Common {
  t: Target
  /** The field's name in the change builders (for example "title", "labels", "cf:12"). */
  field: string
  /** What the control is called (its label and the popover's name). */
  label: string
  session: EditSession
  /** False shows the value as it always was: a read-only repo, or a record still loading. */
  enabled?: boolean
  /** The value as the panel shows it, and the same text for screen readers. */
  display: ReactNode
  valueText: string
}

/** A text, a date or a number. `parse` turns the draft into what the field takes (for example a list split on commas). */
export function TextProp({ t, field, label, session, enabled = true, display, valueText, type = "text", placeholder, required = false, parse }: Common & {
  type?: string; placeholder?: string; required?: boolean; parse?: (draft: string) => unknown
}) {
  const { rec, opts } = session
  const current = fieldValue(t, field, rec, opts)
  const initial = Array.isArray(current) ? current.join(", ") : current == null ? "" : String(current)
  const edit = useFieldEdit((next) => {
    const body = fieldChange(t, field, next, rec, opts)
    return body ? session.save(body) : undefined
  })
  const commit = (draft: string, refocus: boolean) => {
    if (required && !draft.trim()) return edit.cancel(refocus)
    const value = parse ? parse(draft) : draft
    if (!fieldChange(t, field, value, rec, opts)) return edit.cancel(refocus)
    void edit.run(value, refocus)
  }
  return (
    <Field
      edit={edit}
      enabled={enabled}
      label={label}
      valueText={valueText}
      display={display}
      control={<TextControl label={label} initial={initial} type={type} placeholder={placeholder} onCommit={commit} onCancel={(r) => edit.cancel(r)} />}
    />
  )
}

/** A multi-line text (a custom text field): Save and Cancel under it. */
export function AreaProp({ t, field, label, session, enabled = true, display, valueText }: Common) {
  const { rec, opts } = session
  const current = fieldValue(t, field, rec, opts)
  const edit = useFieldEdit((next) => {
    const body = fieldChange(t, field, next, rec, opts)
    return body ? session.save(body) : undefined
  })
  const commit = (draft: string, refocus: boolean) => {
    if (!fieldChange(t, field, draft, rec, opts)) return edit.cancel(refocus)
    void edit.run(draft, refocus)
  }
  return (
    <Field
      edit={edit}
      enabled={enabled}
      label={label}
      valueText={valueText}
      display={display}
      control={<AreaControl label={label} initial={current == null ? "" : String(current)} rows={3} onSave={commit} onCancel={(r) => edit.cancel(r)} />}
    />
  )
}

/**
 * A choice from a list: one value (or a checklist, `multi`). The options carry the values the field takes; the empty
 * choice has the value null (or "" for a custom field, which the form sent that way).
 */
export function ListProp({ t, field, label, session, enabled = true, display, valueText, options, multi = false, popover = true }: Common & {
  options: ListOption[] | null; multi?: boolean; popover?: boolean
}) {
  const { rec, opts } = session
  const current = fieldValue(t, field, rec, opts)
  const selected = multi ? (Array.isArray(current) ? current : []).map(keyOf) : [keyOf(current)]
  const edit = useFieldEdit((next) => {
    const body = fieldChange(t, field, next, rec, opts)
    return body ? session.save(body) : undefined
  })
  const commit = (keys: string[], refocus: boolean) => {
    const pool = options ?? []
    const value = multi
      ? pool.filter((o) => keys.includes(o.key)).map((o) => o.value)
      : (pool.find((o) => o.key === keys[0])?.value ?? null)
    if (!fieldChange(t, field, value, rec, opts)) return edit.cancel(refocus)
    void edit.run(value, refocus)
  }
  return (
    <Field
      edit={edit}
      enabled={enabled}
      label={label}
      valueText={valueText}
      display={display}
      popover={popover}
      control={
        <ListControl
          label={label}
          options={options}
          selected={selected}
          multi={multi}
          onDone={commit}
          onCancel={(r) => edit.cancel(r)}
        />
      }
    />
  )
}

/** A switch: its click saves at once. `custom` is a Redmine yes/no custom field (its values are "1" and "0"). */
export function SwitchProp({ t, field, label, session, custom = false, enabled = true }: Omit<Common, "display" | "valueText"> & { custom?: boolean }) {
  const { rec, opts } = session
  if (!enabled) return <span className="muted">…</span>
  const current = fieldValue(t, field, rec, opts)
  const checked = custom ? current === "1" : current === true
  const edit = useFieldEdit((next) => {
    const body = fieldChange(t, field, next, rec, opts)
    return body ? session.save(body) : undefined
  })
  const toggle = () => {
    const next = custom ? (checked ? "0" : "1") : !checked
    void edit.run(next, true)
  }
  return <SwitchField label={label} checked={checked} edit={edit} onToggle={toggle} />
}
