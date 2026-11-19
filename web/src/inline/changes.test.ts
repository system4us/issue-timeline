import { describe, expect, test } from "vitest"
import {
  attachmentRemove, attachmentsAdd, commentChange, deleteCommentChange, editCommentChange, fieldChange, fieldValue,
  relationAdd, relationRemove, same, targetOf, timeEntryChange, watcherAdd, watcherRemove,
} from "./changes"

// The change builders: given a field, the record as the host has it and the new value, the PATCH body the edit forms
// built (the same field names and conversions). A value that changes nothing sends nothing (null).

const members = [{ id: "alice", name: "alice" }, { id: "bob", name: "bob" }]

describe("Redmine", () => {
  const t = targetOf("redmine:tracker")
  const rec = {
    title: "T", body: "B", state: "OPEN", tracker: { id: 1, name: "Bug" }, priority: { id: 2, name: "Normal" },
    assigneeId: null, versionId: null, categoryId: null, parentId: null, startDate: null, dueDate: "2026-02-01",
    doneRatio: 0, estimatedHours: null, isPrivate: false,
    customFields: [{ id: 12, name: "Area", format: "string", multiple: false, value: "core" }],
    relations: [{ id: 5, type: "blocks", from: 42, to: 7 }],
  }

  test("title, state and references send their own field", () => {
    expect(fieldChange(t, "title", "T", rec)).toBeNull()
    expect(fieldChange(t, "title", "New", rec)).toEqual({ title: "New" })
    expect(fieldChange(t, "state", "CLOSED", rec)).toEqual({ state: "CLOSED" })
    expect(fieldChange(t, "tracker", 2, rec)).toEqual({ tracker: 2 })
    expect(fieldChange(t, "assignee", null, rec)).toBeNull()
    expect(fieldChange(t, "assignee", 4, rec)).toEqual({ assignee: 4 })
    expect(fieldChange(t, "parent", 9, rec)).toEqual({ parent: 9 })
    expect(fieldChange(t, "parent", null, rec)).toBeNull()
    expect(fieldChange(t, "parent", null, { ...rec, parentId: 9 })).toEqual({ parent: null })
  })

  test("dates: an empty box clears the date, a new one sets it", () => {
    expect(fieldChange(t, "startDate", "", rec)).toBeNull()
    expect(fieldChange(t, "startDate", "2026-03-01", rec)).toEqual({ startDate: "2026-03-01" })
    expect(fieldChange(t, "dueDate", "", rec)).toEqual({ dueDate: null })
  })

  test("numbers are sent as numbers; an empty estimate is null", () => {
    expect(fieldChange(t, "doneRatio", "0", rec)).toBeNull()
    expect(fieldChange(t, "doneRatio", "30", rec)).toEqual({ doneRatio: 30 })
    expect(fieldChange(t, "estimatedHours", "", rec)).toBeNull()
    expect(fieldChange(t, "estimatedHours", "2.5", rec)).toEqual({ estimatedHours: 2.5 })
    expect(fieldChange(t, "isPrivate", true, rec)).toEqual({ isPrivate: true })
  })

  test("a custom field goes under customFields, by its id", () => {
    expect(fieldValue(t, "cf:12", rec)).toBe("core")
    expect(fieldValue(t, "cf:99", rec)).toBe("")
    expect(fieldChange(t, "cf:12", "core", rec)).toBeNull()
    expect(fieldChange(t, "cf:12", "edge", rec)).toEqual({ customFields: { 12: "edge" } })
  })

  test("comments, a private note, relations, watchers, files and time", () => {
    expect(commentChange("  hi ")).toEqual({ comment: "hi" })
    expect(commentChange("hi", true)).toEqual({ comment: "hi", privateNote: true })
    expect(commentChange("   ")).toBeNull()
    expect(relationAdd(t, "relates", "7")).toEqual({ addRelations: [{ type: "relates", to: 7 }] })
    expect(relationRemove(t, { id: 5 })).toEqual({ removeRelations: [5] })
    expect(watcherAdd(3)).toEqual({ addWatchers: [3] })
    expect(watcherRemove(3)).toEqual({ removeWatchers: [3] })
    const file = { filename: "a.txt", contentType: "text/plain", data: "YQ==" }
    expect(attachmentsAdd([file])).toEqual({ attachments: [file] })
    expect(attachmentRemove(8)).toEqual({ removeAttachments: [8] })
    expect(timeEntryChange({ hours: "1.5", activity: "9", comment: " tests " })).toEqual({
      timeEntry: { hours: 1.5, activityId: 9, comments: "tests" },
    })
  })
})

describe("GitHub issue", () => {
  const t = targetOf("github:owner/repo")
  const rec = {
    title: "T", body: "B", state: "OPEN", labels: [{ name: "bug", color: "f00" }], assignees: ["alice"],
    milestone: { title: "v1" }, parent: "", locked: false, issueType: "Bug", stateReason: null,
  }
  const opts = {
    members, labels: [{ name: "bug", color: "f00" }, { name: "ui", color: "0f0" }],
    milestones: [{ id: 3, name: "v1" }, { id: 4, name: "v2" }], issueTypes: [{ id: 1, name: "Bug" }, { id: 2, name: "Feature" }],
  } as any

  test("labels, assignees and milestone send the names and ids the host takes", () => {
    expect(fieldChange(t, "labels", ["bug"], rec, opts)).toBeNull()
    expect(fieldChange(t, "labels", ["bug", "ui"], rec, opts)).toEqual({ labels: ["bug", "ui"] })
    expect(fieldValue(t, "assignees", rec, opts)).toEqual(["alice"])
    expect(fieldChange(t, "assignees", ["alice", "bob"], rec, opts)).toEqual({ assignees: ["alice", "bob"] })
    expect(fieldValue(t, "milestone", rec, opts)).toBe(3)
    expect(fieldChange(t, "milestone", null, rec, opts)).toEqual({ milestone: null })
    expect(fieldChange(t, "milestone", 3, rec, opts)).toBeNull()
  })

  test("the state menu sends the reason with a close, and nothing else for a reopen", () => {
    expect(fieldChange(t, "state", "CLOSED:completed", rec, opts)).toEqual({ state: "CLOSED", stateReason: "completed" })
    const closed = { ...rec, state: "CLOSED", stateReason: "NOT_PLANNED" }
    expect(fieldValue(t, "state", closed)).toBe("CLOSED:not_planned")
    expect(fieldChange(t, "state", "CLOSED:not_planned", closed, opts)).toBeNull()
    expect(fieldChange(t, "state", "OPEN", closed, opts)).toEqual({ state: "OPEN" })
  })

  test("parent, type and lock", () => {
    expect(fieldChange(t, "parent", "", rec, opts)).toBeNull()
    expect(fieldChange(t, "parent", "12", rec, opts)).toEqual({ parent: "12" })
    expect(fieldChange(t, "parent", "", { ...rec, parent: "12" }, opts)).toEqual({ parent: null })
    expect(fieldChange(t, "type", "Feature", rec, opts)).toEqual({ type: "Feature" })
    expect(fieldChange(t, "type", null, rec, opts)).toEqual({ type: null })
    expect(fieldChange(t, "type", null, { ...rec, issueType: null }, opts)).toBeNull()
    expect(fieldChange(t, "locked", true, rec, opts)).toEqual({ locked: true })
  })

  test("a field of another host sends nothing here", () => {
    expect(fieldChange(t, "weight", "3", rec, opts)).toBeNull()
    expect(fieldChange(t, "dueDate", "2026-01-01", rec, opts)).toBeNull()
  })

  test("editing and deleting a note", () => {
    expect(editCommentChange("7", "x")).toEqual({ editComments: [{ id: "7", body: "x" }] })
    expect(deleteCommentChange("7")).toEqual({ deleteComments: ["7"] })
  })
})

describe("GitLab issue", () => {
  const t = targetOf("gitlab:group/project")
  const rec = { title: "T", body: "B", state: "OPEN", labels: [], assignees: [], milestone: null, parent: "", confidential: false, weight: null, dueDate: null }

  test("weight is a number, and clearing it sends null", () => {
    expect(fieldChange(t, "weight", "", rec)).toBeNull()
    expect(fieldChange(t, "weight", "3", rec)).toEqual({ weight: 3 })
    expect(fieldChange(t, "weight", "", { ...rec, weight: 2 })).toEqual({ weight: null })
  })

  test("confidential, due date and the time fields", () => {
    expect(fieldChange(t, "confidential", true, rec)).toEqual({ confidential: true })
    expect(fieldChange(t, "dueDate", "2026-05-01", rec)).toEqual({ dueDate: "2026-05-01" })
    expect(fieldChange(t, "dueDate", "", rec)).toBeNull()
    expect(fieldChange(t, "timeEstimate", "2h", rec)).toEqual({ timeEstimate: "2h" })
    expect(fieldChange(t, "timeEstimate", "  ", rec)).toBeNull()
    expect(fieldChange(t, "addSpent", "30m", rec)).toEqual({ addSpent: "30m" })
  })

  test("GitHub's type and lock are not GitLab's", () => {
    expect(fieldChange(t, "type", "Bug", rec)).toBeNull()
    expect(fieldChange(t, "locked", true, rec)).toBeNull()
  })

  test("links: relates to, and the blocked-by link to remove", () => {
    expect(relationAdd(t, "relates", "4")).toEqual({ addLinks: [{ type: "relates", to: "4" }] })
    expect(relationRemove(t, { type: "blockedBy", to: "4" })).toEqual({ removeLinks: [{ type: "blockedBy", to: "4" }] })
  })
})

describe("pull request", () => {
  const t = targetOf("github:owner/repo", true)
  const rec = { title: "T", body: "B", state: "OPEN", labels: [], assignees: [], milestone: null, isDraft: false, reviewers: ["bob"] }
  const opts = { members } as any

  test("draft and reviewers", () => {
    expect(fieldChange(t, "isDraft", false, rec, opts)).toBeNull()
    expect(fieldChange(t, "isDraft", true, rec, opts)).toEqual({ isDraft: true })
    expect(fieldValue(t, "reviewers", rec, opts)).toEqual(["bob"])
    expect(fieldChange(t, "reviewers", ["bob"], rec, opts)).toBeNull()
    expect(fieldChange(t, "reviewers", ["alice", "bob"], rec, opts)).toEqual({ reviewers: ["alice", "bob"] })
  })

  test("state is open or closed, and a pull request has no parent or due date", () => {
    expect(fieldChange(t, "state", "CLOSED", rec, opts)).toEqual({ state: "CLOSED" })
    expect(fieldChange(t, "parent", "12", rec, opts)).toBeNull()
    expect(fieldChange(t, "dueDate", "2026-01-01", rec, opts)).toBeNull()
  })

  test("the comment goes to the pull request as a comment", () => {
    expect(commentChange("ok")).toEqual({ comment: "ok" })
  })
})

describe("same", () => {
  test("null and undefined are alike, and lists compare whatever their order", () => {
    expect(same(null, undefined)).toBe(true)
    expect(same(["a", "b"], ["b", "a"])).toBe(true)
    expect(same(["a"], ["a", "b"])).toBe(false)
    expect(same(1, "1")).toBe(false)
  })
})
