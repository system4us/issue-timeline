import { test } from "node:test";
import assert from "node:assert/strict";
import { createClient } from "./github.js";
import { normalizeIssue } from "./model.js";

const fakeFetch = (body) => async () => ({ ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) });

test("a NOT_FOUND on one node keeps the rest of the data", async () => {
  const graphql = createClient({ token: "t", fetchImpl: fakeFetch({ data: { repository: { x: 1 } }, errors: [{ type: "NOT_FOUND", message: "Could not resolve" }] }) });
  assert.deepEqual(await graphql("query"), { repository: { x: 1 } });
});

test("any other GraphQL error still fails the call", async () => {
  const graphql = createClient({ token: "t", fetchImpl: fakeFetch({ data: null, errors: [{ type: "FORBIDDEN", message: "no" }] }) });
  await assert.rejects(graphql("query"), /GitHub GraphQL: no/);
});

test("a connection that came back null reads as empty, not as a crash", () => {
  const issue = normalizeIssue({
    number: 3, title: "t", body: "", url: "u", state: "OPEN", createdAt: "2026-01-01T00:00:00Z", closedAt: null, author: { login: "a" },
    labels: null, assignees: null, milestone: null, comments: null, timelineItems: null,
    parent: null, subIssues: null, blockedBy: null, blocking: null, stateReason: null,
  });
  assert.deepEqual(issue.labels, []);
  assert.deepEqual(issue.events, []);
  assert.deepEqual(issue.blockedBy, []);
});

test("a body cut short is retried, and the next good answer is used", async () => {
  let calls = 0;
  const fetchImpl = async () => {
    calls++;
    if (calls === 1) return { ok: true, status: 200, json: async () => { throw new SyntaxError("Unterminated string in JSON"); } };
    return { ok: true, status: 200, json: async () => ({ data: { ok: true } }) };
  };
  const graphql = createClient({ token: "t", fetchImpl });
  assert.deepEqual(await graphql("query"), { ok: true });
  assert.equal(calls, 2);
});

import { githubEndpoints } from "./github.js";

test("endpoints: github.com by default, GitHub Enterprise through GITHUB_HOST", () => {
  assert.deepEqual(githubEndpoints({}), { host: "github.com", rest: "https://api.github.com", graphql: "https://api.github.com/graphql" });
  const ghe = githubEndpoints({ GITHUB_HOST: "https://ghe.example.com/" });
  assert.equal(ghe.rest, "https://ghe.example.com/api/v3");
  assert.equal(ghe.graphql, "https://ghe.example.com/api/graphql");
});
