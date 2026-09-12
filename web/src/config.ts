// Bun-side mirror of internal/config (Go). Loads config.json from the
// same search path the Go CLI uses, applies the same EXAMTOPICS_*
// environment overrides, and resolves user-relative paths so both
// runtimes operate on identical settings when run from the same
// machine. See §3.5 of docs/plans/portable-builds.md for the contract.
//
// The hostId auto-generation / persistence step that internal/config
// performs is intentionally NOT mirrored here yet: Bun side only writes
// the multi-host-tagged tables once task 4-E rewrites db.ts to use
// UUIDv7 BLOB primary keys. Until then loadConfig leaves hostId blank
// and the Go CLI is the only writer of host-tagged rows.

import { existsSync, readFileSync } from "node:fs";
import { resolve, dirname, join } from "node:path";

export type WebSection = {
  host: string;
  port: number;
  adminEnabled: boolean;
};

export type ScrapeSection = {
  defaultProvider: string;
  noCache: boolean;
};

// TranslateSection mirrors internal/config (Go) — drives which LLM CLI
// the spawned `examtopicsdl translate` invocation talks to and (when
// non-empty) the model flag passed through. Web doesn't make the
// dispatch decision itself; the Go binary reads its own config.json.
// The mirror exists so future TS code that wants to display "active
// client" / "active model" gets typed access without re-parsing.
export type TranslateSection = {
  client: string;
  model: string;
  bin: string;
};

export type ToolsSection = {
  translate: TranslateSection;
};

export type Config = {
  hostId: string;
  dataDir: string;
  logDir: string;
  downloaderBin: string;
  web: WebSection;
  scrape: ScrapeSection;
  tools: ToolsSection;
  loadedFrom: string;
};

const FORBIDDEN_KEYS = [
  "ghPat",
  "GH_PAT",
  "token",
  "Token",
  "adminToken",
  "EXAMTOPICS_ADMIN_TOKEN",
  "apiKey",
  "secret",
];

function defaults(): Config {
  return {
    hostId: "",
    dataDir: "",
    logDir: "",
    downloaderBin: "",
    web: { host: "127.0.0.1", port: 8787, adminEnabled: true },
    scrape: { defaultProvider: "amazon", noCache: false },
    tools: { translate: { client: "", model: "", bin: "" } },
    loadedFrom: "",
  };
}

/**
 * UserConfigDir returns the platform-conventional per-user config
 * directory for examtopics, matching internal/config.UserConfigDir
 * (Go). Empty string means none of the supported environment vars
 * are set (HOME / USERPROFILE / APPDATA / XDG_CONFIG_HOME).
 */
export function userConfigDir(): string {
  if (process.platform === "win32" && process.env.APPDATA) {
    return join(process.env.APPDATA, "examtopics");
  }
  if (process.env.XDG_CONFIG_HOME) {
    return join(process.env.XDG_CONFIG_HOME, "examtopics");
  }
  const home = process.env.HOME ?? process.env.USERPROFILE;
  return home ? join(home, ".config", "examtopics") : "";
}

function candidatePaths(): string[] {
  const out: string[] = [];
  if (process.env.EXAMTOPICS_CONFIG) out.push(process.env.EXAMTOPICS_CONFIG);
  out.push(join(process.cwd(), "config.json"));
  if (process.execPath) {
    out.push(join(dirname(process.execPath), "config.json"));
  }
  const dir = userConfigDir();
  if (dir) out.push(join(dir, "config.json"));
  return out;
}

function stripForbidden(raw: Record<string, unknown>, source: string): Record<string, unknown> {
  for (const k of FORBIDDEN_KEYS) {
    if (k in raw) {
      console.warn(
        `[config] warning: key "${k}" in ${source} is not allowed — use .env or environment variable instead. Ignoring.`
      );
      delete raw[k];
    }
  }
  return raw;
}

function applyEnvOverrides(cfg: Config): void {
  if (process.env.EXAMTOPICS_HOST_ID) cfg.hostId = process.env.EXAMTOPICS_HOST_ID;
  if (process.env.EXAMTOPICS_DATA_DIR) cfg.dataDir = process.env.EXAMTOPICS_DATA_DIR;
  if (process.env.EXAMTOPICS_LOG_DIR) cfg.logDir = process.env.EXAMTOPICS_LOG_DIR;
  if (process.env.EXAMTOPICS_DOWNLOADER_BIN) cfg.downloaderBin = process.env.EXAMTOPICS_DOWNLOADER_BIN;
  if (process.env.EXAMTOPICS_TRANSLATE_CLIENT) cfg.tools.translate.client = process.env.EXAMTOPICS_TRANSLATE_CLIENT;
  if (process.env.EXAMTOPICS_TRANSLATE_MODEL) cfg.tools.translate.model = process.env.EXAMTOPICS_TRANSLATE_MODEL;
  if (process.env.EXAMTOPICS_TRANSLATE_BIN) cfg.tools.translate.bin = process.env.EXAMTOPICS_TRANSLATE_BIN;
}

function expandPath(s: string): string {
  if (!s) return s;
  if (s === "~" || s.startsWith("~/") || s.startsWith("~\\")) {
    const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
    return resolve(home, s.replace(/^~[/\\]?/, ""));
  }
  return resolve(s);
}

let cached: Config | null = null;

/**
 * Load the runtime config. Missing config.json is not an error —
 * defaults plus env overrides give a sensible starting point. The
 * result is memoized; tests that mutate process.env should call
 * resetConfigCache() to force a reload.
 */
export function loadConfig(): Config {
  if (cached) return cached;
  const cfg = defaults();
  for (const p of candidatePaths()) {
    if (!existsSync(p)) continue;
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(readFileSync(p, "utf-8"));
    } catch (e) {
      throw new Error(`config: parse ${p}: ${e instanceof Error ? e.message : String(e)}`);
    }
    const cleaned = stripForbidden(parsed, p);
    Object.assign(cfg, cleaned);
    cfg.loadedFrom = p;
    break;
  }
  applyEnvOverrides(cfg);
  cfg.dataDir = expandPath(cfg.dataDir) || process.cwd();
  cfg.logDir = expandPath(cfg.logDir) || join(cfg.dataDir, "logs");
  cfg.downloaderBin = expandPath(cfg.downloaderBin);
  cached = cfg;
  return cfg;
}

/** Drop the cached config so a subsequent loadConfig() re-reads fresh state. */
export function resetConfigCache(): void {
  cached = null;
}
