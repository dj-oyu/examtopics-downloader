// Client-side TS sources are imported as text at build time so the
// compiled Bun binary can serve them without reading from the filesystem
// (§2 notice 6 of docs/plans/portable-builds.md). The previous
// readFileSync + import.meta.dir + mtime-cache approach is replaced by a
// static lookup table keyed by script name; the runtime work is just the
// browser-target transpile, cached after the first call per script.
// TypeScript treats `.ts` files as modules and rejects the import as
// "not a module" because client/*.ts files are top-level scripts with no
// exports. Bun's `with { type: "text" }` returns the raw source as a
// string at runtime regardless of TS's view, so we suppress per-import
// and assert the result is a string locally. The suppressions are
// scoped to these two lines only.
// @ts-ignore: bun text import attribute returns the raw source as a string
import threadLiveImport from "./thread-live.ts" with { type: "text" };
// @ts-ignore: bun text import attribute returns the raw source as a string
import questionLiveImport from "./question-live.ts" with { type: "text" };
// @ts-ignore: bun text import attribute returns the raw source as a string
import adminFetchImport from "./admin-fetch.ts" with { type: "text" };
const threadLiveSource: string = threadLiveImport;
const questionLiveSource: string = questionLiveImport;
const adminFetchSource: string = adminFetchImport;

const transpiler = new Bun.Transpiler({ loader: "ts", target: "browser" });

const sources: Record<string, string> = {
  "thread-live": threadLiveSource,
  "question-live": questionLiveSource,
  "admin-fetch": adminFetchSource,
};

const cache = new Map<string, string>();

/**
 * Read a TypeScript client script (registered in the sources map above)
 * and return the transpiled browser-target JS source. Each script is
 * transpiled at most once per process; the result is held in the cache.
 *
 * Adding a new script: import it as text at the top of this file and
 * append it to the sources map.
 */
export function loadClientScript(name: string): string {
  const hit = cache.get(name);
  if (hit !== undefined) return hit;
  const src = sources[name];
  if (src === undefined) {
    throw new Error(
      `unknown client script: ${name} (registered: ${Object.keys(sources).join(", ")})`
    );
  }
  const js = transpiler.transformSync(src);
  cache.set(name, js);
  return js;
}
