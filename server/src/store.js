// A mini store in one JSON file: { meta, issues: {number: Issue}, pulls: {number: Pull} }.
// Writes are atomic (temp file + rename) and serialized, so a crash never leaves half a file.
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

const EMPTY = () => ({ meta: { repo: null, lastSync: null, fullSyncAt: null, syncs: [] }, issues: {}, pulls: {} });

export class Store {
  /** @param {string} file */
  constructor(file) {
    this.file = file;
    this.data = EMPTY();
    this.writing = Promise.resolve();
  }

  async load() {
    try {
      const parsed = JSON.parse(await readFile(this.file, "utf8"));
      this.data = { ...EMPTY(), ...parsed, meta: { ...EMPTY().meta, ...parsed.meta } };
    } catch (e) {
      if (e.code !== "ENOENT") throw e;
    }
    return this;
  }

  /** Inserts or replaces by number; returns how many were new. */
  upsert(kind, items) {
    const bucket = this.data[kind];
    let added = 0;
    for (const item of items) {
      if (!bucket[item.number]) added++;
      bucket[item.number] = item;
    }
    return added;
  }

  save() {
    // Serialize writes: each one waits for the previous and writes the state at its own turn.
    this.writing = this.writing.then(async () => {
      await mkdir(dirname(this.file), { recursive: true });
      const tmp = `${this.file}.tmp`;
      await writeFile(tmp, JSON.stringify(this.data));
      await rename(tmp, this.file);
    });
    return this.writing;
  }
}
