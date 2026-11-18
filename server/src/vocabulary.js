// The words issue bodies use: dependencies, field keys and CI workflow names. The defaults live in vocabulary.json (several
// languages); VOCABULARY_FILE, a JSON file with the same keys, replaces the lists it names. Shared by the server (derive,
// links, CI) and, through the payload, by the frontend (fields and dependency rows).
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const DEFAULTS = JSON.parse(readFileSync(fileURLToPath(new URL("./vocabulary.json", import.meta.url)), "utf8"));

/** The defaults with the file of VOCABULARY_FILE (if any) applied over them. Throws on a file that is not valid. */
export function loadVocabulary(file = process.env.VOCABULARY_FILE) {
  if (!file) return DEFAULTS;
  let custom;
  try { custom = JSON.parse(readFileSync(file, "utf8")); } catch (e) {
    throw new Error(`VOCABULARY_FILE ${file} is not a readable JSON file: ${e.message}`);
  }
  const out = structuredClone(DEFAULTS);
  for (const key of ["blockedBy", "blocks", "fieldKeys"]) if (Array.isArray(custom[key])) out[key] = custom[key];
  if (custom.ci?.otherWorkflows) out.ci.otherWorkflows = custom.ci.otherWorkflows;
  return out;
}

const escape = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
/** A list of phrases as one alternation, longest first, at word boundaries. Matches nothing for an empty list. */
const alternation = (phrases) => {
  const list = [...new Set(phrases.map((p) => p.trim()).filter(Boolean))].sort((a, b) => b.length - a.length);
  return list.length ? `(?:${list.map(escape).join("|")})` : "(?!)";
};

// A phrase starts and ends where a letter or digit does not: plain \\b fails after a ")" and misreads "bloqué".
const START = "(?<![\\p{L}\\p{N}_])";
const END = "(?![\\p{L}\\p{N}_])";

/** The regular expressions the server uses, compiled from a vocabulary. */
export function compile(vocab = vocabulary) {
  return {
    blockedBy: new RegExp(`${START}${alternation(vocab.blockedBy)}${END}([^\\n.;]{0,140})`, "giu"),
    blocks: new RegExp(`${START}${alternation(vocab.blocks)}${END}([^\\n.;]{0,140})`, "giu"),
    otherWorkflows: new RegExp(`${START}${alternation(vocab.ci.otherWorkflows)}${END}`, "iu"),
  };
}

export const vocabulary = loadVocabulary();
