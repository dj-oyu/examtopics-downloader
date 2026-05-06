import { describe, expect, test, beforeEach, afterEach } from "bun:test";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { loadConfig, resetConfigCache, userConfigDir } from "./config";

let workDir = "";
const savedEnv: Record<string, string | undefined> = {};

const ENV_KEYS = [
  "EXAMTOPICS_CONFIG",
  "EXAMTOPICS_DATA_DIR",
  "EXAMTOPICS_LOG_DIR",
  "EXAMTOPICS_HOST_ID",
  "EXAMTOPICS_DOWNLOADER_BIN",
  "HOME",
  "USERPROFILE",
  "APPDATA",
  "XDG_CONFIG_HOME",
];

function isolate() {
  workDir = join(tmpdir(), `cfgtest-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(workDir, { recursive: true });
  for (const k of ENV_KEYS) {
    savedEnv[k] = process.env[k];
    delete process.env[k];
  }
  process.env.HOME = workDir;
  process.env.USERPROFILE = workDir;
  process.env.APPDATA = join(workDir, "AppData");
  process.env.XDG_CONFIG_HOME = join(workDir, ".config");
  resetConfigCache();
}

function restore() {
  for (const k of ENV_KEYS) {
    if (savedEnv[k] === undefined) delete process.env[k];
    else process.env[k] = savedEnv[k];
  }
  resetConfigCache();
  if (workDir) rmSync(workDir, { recursive: true, force: true });
}

describe("loadConfig", () => {
  beforeEach(isolate);
  afterEach(restore);

  test("returns defaults when no config.json exists", () => {
    const cfg = loadConfig();
    expect(cfg.web.host).toBe("127.0.0.1");
    expect(cfg.web.port).toBe(8787);
    expect(cfg.web.adminEnabled).toBe(true);
    expect(cfg.scrape.defaultProvider).toBe("amazon");
    expect(cfg.dataDir).toBeTruthy();
    expect(cfg.loadedFrom).toBe("");
  });

  test("reads explicit config from EXAMTOPICS_CONFIG env", () => {
    const cfgPath = join(workDir, "myconfig.json");
    writeFileSync(
      cfgPath,
      JSON.stringify({ hostId: "fixture-host", web: { port: 9999 } })
    );
    process.env.EXAMTOPICS_CONFIG = cfgPath;
    resetConfigCache();
    const cfg = loadConfig();
    expect(cfg.hostId).toBe("fixture-host");
    expect(cfg.web.port).toBe(9999);
    expect(cfg.loadedFrom).toBe(cfgPath);
  });

  test("env overrides win over JSON values", () => {
    const cfgPath = join(workDir, "config.json");
    writeFileSync(cfgPath, JSON.stringify({ hostId: "from-json", dataDir: join(workDir, "from-json") }));
    process.env.EXAMTOPICS_CONFIG = cfgPath;
    process.env.EXAMTOPICS_HOST_ID = "from-env";
    process.env.EXAMTOPICS_DATA_DIR = join(workDir, "from-env");
    resetConfigCache();
    const cfg = loadConfig();
    expect(cfg.hostId).toBe("from-env");
    expect(cfg.dataDir).toBe(join(workDir, "from-env"));
  });

  test("forbidden keys are stripped with a warning", () => {
    const cfgPath = join(workDir, "config.json");
    writeFileSync(
      cfgPath,
      JSON.stringify({
        hostId: "with-secret",
        ghPat: "redacted-fake",
        adminToken: "redacted-fake",
        dataDir: join(workDir, "safe"),
      })
    );
    process.env.EXAMTOPICS_CONFIG = cfgPath;
    resetConfigCache();
    const cfg = loadConfig();
    expect(cfg.hostId).toBe("with-secret");
    expect((cfg as unknown as Record<string, unknown>).ghPat).toBeUndefined();
    expect((cfg as unknown as Record<string, unknown>).adminToken).toBeUndefined();
  });

  test("dataDir defaults to cwd when neither config nor env sets it", () => {
    const cfg = loadConfig();
    expect(cfg.dataDir).toBe(process.cwd());
  });

  test("logDir defaults to <dataDir>/logs when only dataDir is set", () => {
    process.env.EXAMTOPICS_DATA_DIR = workDir;
    resetConfigCache();
    const cfg = loadConfig();
    expect(cfg.logDir).toBe(join(workDir, "logs"));
  });
});

describe("userConfigDir", () => {
  beforeEach(isolate);
  afterEach(restore);

  test("returns a non-empty path under the isolated home", () => {
    const dir = userConfigDir();
    expect(dir).toBeTruthy();
    expect(dir.toLowerCase()).toContain("examtopics");
  });
});
