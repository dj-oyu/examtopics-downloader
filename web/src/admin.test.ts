import {
  describe,
  expect,
  test,
  beforeEach,
  afterEach,
  beforeAll,
} from "bun:test";
import { mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { resetConfigCache } from "./config";

// All admin module unit tests run in an isolated tmp dataDir so they
// don't accidentally find the developer's *.db files at process.cwd().
let workDir = "";
const savedEnv: Record<string, string | undefined> = {};

const ENV_KEYS = [
  "EXAMTOPICS_CONFIG",
  "EXAMTOPICS_DATA_DIR",
  "EXAMTOPICS_LOG_DIR",
  "EXAMTOPICS_HOST_ID",
  "EXAMTOPICS_DOWNLOADER_BIN",
  "EXAMTOPICSDL_BIN",
  "EXAMTOPICS_ADMIN_TOKEN",
  "HOME",
  "USERPROFILE",
  "APPDATA",
  "XDG_CONFIG_HOME",
];

function isolate() {
  workDir = join(
    tmpdir(),
    `admintest-${Date.now()}-${Math.random().toString(36).slice(2)}`
  );
  mkdirSync(workDir, { recursive: true });
  for (const k of ENV_KEYS) {
    savedEnv[k] = process.env[k];
    delete process.env[k];
  }
  process.env.HOME = workDir;
  process.env.USERPROFILE = workDir;
  process.env.APPDATA = join(workDir, "AppData");
  process.env.XDG_CONFIG_HOME = join(workDir, ".config");
  process.env.EXAMTOPICS_DATA_DIR = workDir;
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

describe("isValidSlug", () => {
  beforeAll(async () => {
    // Re-import after isolate so the module sees a clean env. The
    // cached module here is fine — slug validation has no env deps.
  });

  test("accepts well-formed slugs", async () => {
    const { isValidSlug } = await import("./admin");
    expect(isValidSlug("saa-c03")).toBe(true);
    expect(isValidSlug("dop-c02")).toBe(true);
    expect(isValidSlug("a.b_c-1")).toBe(true);
  });

  test("rejects bad shapes", async () => {
    const { isValidSlug } = await import("./admin");
    expect(isValidSlug("")).toBe(false);
    expect(isValidSlug(".")).toBe(false);
    expect(isValidSlug("..")).toBe(false);
    expect(isValidSlug("a/b")).toBe(false);
    expect(isValidSlug("../etc/passwd")).toBe(false);
    expect(isValidSlug("has space")).toBe(false);
  });
});

describe("tokenMatches / getAdminToken", () => {
  beforeEach(isolate);
  afterEach(restore);

  test("returns null when EXAMTOPICS_ADMIN_TOKEN is unset", async () => {
    const { getAdminToken, tokenMatches } = await import("./admin");
    expect(getAdminToken()).toBeNull();
    expect(tokenMatches("anything")).toBe(false);
    expect(tokenMatches("")).toBe(false);
  });

  test("matches the configured token via constant-time compare", async () => {
    process.env.EXAMTOPICS_ADMIN_TOKEN = "shh-super-secret";
    const { getAdminToken, tokenMatches } = await import("./admin");
    expect(getAdminToken()).toBe("shh-super-secret");
    expect(tokenMatches("shh-super-secret")).toBe(true);
    expect(tokenMatches("shh-super-secre")).toBe(false); // length differs
    expect(tokenMatches("shh-super-decoy0")).toBe(false); // same length
    expect(tokenMatches(null)).toBe(false);
  });
});

describe("resolveDownloaderBin", () => {
  beforeEach(isolate);
  afterEach(restore);

  test("honors EXAMTOPICSDL_BIN env when it points at an existing file", async () => {
    const fake = join(workDir, "fake-bin");
    await Bun.write(fake, "#!/bin/sh\nexit 0\n");
    process.env.EXAMTOPICSDL_BIN = fake;
    const { resolveDownloaderBin, resetDownloaderBinCache } = await import(
      "./admin"
    );
    resetDownloaderBinCache();
    expect(resolveDownloaderBin()).toBe(fake);
  });

  test("throws a clear error when nothing resolves", async () => {
    // Point env at a path that doesn't exist; clear PATH so Bun.which
    // can't find a system examtopicsdl.
    process.env.EXAMTOPICSDL_BIN = join(workDir, "nope");
    const savedPath = process.env.PATH;
    process.env.PATH = "";
    try {
      const { resolveDownloaderBin, resetDownloaderBinCache } = await import(
        "./admin"
      );
      resetDownloaderBinCache();
      expect(() => resolveDownloaderBin()).toThrow(/not found/i);
    } finally {
      if (savedPath !== undefined) process.env.PATH = savedPath;
    }
  });
});

describe("startFetch", () => {
  beforeEach(isolate);
  afterEach(async () => {
    const { resetFetchStateForTests } = await import("./admin");
    resetFetchStateForTests();
    restore();
  });

  test("rejects when a job is already in flight", async () => {
    const admin = await import("./admin");
    admin.installFakeJobForTests({ provider: "amazon", slug: "saa-c03" });
    const result = admin.startFetch({
      provider: "amazon",
      slug: "saa-c03",
    });
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.status).toBe(409);
      expect(result.message).toContain("in flight");
    }
  });

  test("rejects when the binary cannot be resolved", async () => {
    const savedPath = process.env.PATH;
    process.env.PATH = "";
    process.env.EXAMTOPICSDL_BIN = join(workDir, "missing");
    try {
      const admin = await import("./admin");
      admin.resetDownloaderBinCache();
      admin.resetFetchStateForTests();
      const result = admin.startFetch({
        provider: "amazon",
        slug: "saa-c03",
      });
      expect(result.ok).toBe(false);
      if (!result.ok) {
        expect(result.status).toBe(503);
        expect(result.message).toMatch(/not found/i);
      }
    } finally {
      if (savedPath !== undefined) process.env.PATH = savedPath;
    }
  });
});

describe("subscribeFetch / fake job", () => {
  beforeEach(isolate);
  afterEach(async () => {
    const { resetFetchStateForTests } = await import("./admin");
    resetFetchStateForTests();
    restore();
  });

  test("replays buffered log lines on subscribe", async () => {
    const admin = await import("./admin");
    admin.installFakeJobForTests({
      provider: "amazon",
      slug: "saa-c03",
      log: [
        { stream: "stdout", text: "hello", ts: 1 },
        { stream: "stderr", text: "warn", ts: 2 },
      ],
      done: { exitCode: 0, ts: 3 },
    });
    const events: unknown[] = [];
    const unsub = admin.subscribeFetch((e) => events.push(e));
    unsub();
    expect(events.length).toBe(3);
    // The first two should be log lines, the third the done event.
    const types = events.map((e) => (e as { type: string }).type);
    expect(types).toEqual(["line", "line", "done"]);
  });
});
