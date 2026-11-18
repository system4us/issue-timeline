import { test } from "node:test";
import assert from "node:assert/strict";
import { applyHierarchy, gitlabHierarchy } from "./gitlab.js";

const item = (iid, widgets) => ({ iid: String(iid), workItemType: { name: "Issue" }, widgets });
const hier = (parent, children = []) => ({ __typename: "WorkItemWidgetHierarchy", parent, children: { nodes: children.map((c) => ({ iid: String(c) })) } });
const epic = { iid: "486", title: "Feature parity", webUrl: "https://gitlab.com/groups/g/-/work_items/486", workItemType: { name: "Epic" } };

test("the hierarchy reads two pages of work items, with epic parents and children", async () => {
  process.env.GITLAB_TOKEN = "test-token";
  const pages = [
    { data: { project: { workItems: { pageInfo: { hasNextPage: true, endCursor: "c1" }, nodes: [item(180, [hier(epic)]), item(5, [hier(null, [6])])] } } } },
    { data: { project: { workItems: { pageInfo: { hasNextPage: false, endCursor: null }, nodes: [item(6, [hier({ iid: "5", title: "t", webUrl: "u", workItemType: { name: "Issue" } })])] } } } },
  ];
  const seen = [];
  const fetchImpl = async (url, opts) => {
    seen.push(JSON.parse(opts.body).variables.after);
    const body = pages.shift();
    return { ok: true, status: 200, json: async () => body };
  };
  const h = await gitlabHierarchy({ repoKey: "gitlab:https://gitlab.com/g/p", fetchImpl });
  assert.deepEqual(seen, [null, "c1"], "the second page is asked with the first page's cursor");
  assert.equal(h.get(180).parent.iid, 486);
  assert.equal(h.get(180).parent.type, "Epic");
  assert.deepEqual(h.get(5).children, [6]);
  assert.equal(h.get(6).parent.iid, 5);
});

test("applyHierarchy: an issue parent goes to parent, an epic to parentEpic, and children that are issues to subIssues", () => {
  const issues = { 5: { number: 5 }, 6: { number: 6 }, 180: { number: 180 } };
  const h = new Map([
    [180, { type: "Issue", parent: { iid: 486, title: "Feature parity", url: "u", type: "Epic" }, children: [] }],
    [5, { type: "Issue", parent: null, children: [6, 999] }],
    [6, { type: "Issue", parent: { iid: 5, title: "t", url: "u", type: "Issue" }, children: [] }],
  ]);
  applyHierarchy(issues, h);
  assert.equal(issues[180].parent, null);
  assert.equal(issues[180].parentEpic.iid, 486);
  assert.equal(issues[6].parent, 5);
  assert.equal(issues[6].parentEpic, null);
  assert.deepEqual(issues[5].subIssues, [6], "a child that is not a stored issue is left out");
});
