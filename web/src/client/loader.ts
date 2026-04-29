import { readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";

const CLIENT_DIR = import.meta.dir;
const transpiler = new Bun.Transpiler({ loader: "ts", target: "browser" });

type CacheEntry = { mtimeMs: number; js: string };
const cache = new Map<string, CacheEntry>();

/**
 * Read a TypeScript file from src/client/ and return the transpiled JS source.
 *
 * Caches by file mtime so edits to the client script are picked up on the
 * next SSR render — `bun --hot` doesn't see these files because they're
 * read as text (not imported), so we have to invalidate ourselves.
 */
export function loadClientScript(name: string): string {
  const path = resolve(CLIENT_DIR, `${name}.ts`);
  const { mtimeMs } = statSync(path);
  const cached = cache.get(name);
  if (cached && cached.mtimeMs === mtimeMs) return cached.js;
  const ts = readFileSync(path, "utf-8");
  const js = transpiler.transformSync(ts);
  cache.set(name, { mtimeMs, js });
  return js;
}
