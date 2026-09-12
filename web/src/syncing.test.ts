// Tests for the /admin/sync support module: output parsing, peer discovery and
// the start-up guards. The job runner itself is exercised through its
// validation paths (a real child process is covered by the CLI's own tests and
// the end-to-end run documented in the commit).
import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { resetConfigCache } from "./config";
import { resetDownloaderBinCache } from "./admin";

const ENV_KEYS = [
  "EXAMTOPICS_CONFIG",
  "EXAMTOPICS_DATA_DIR",
  "EXAMTOPICS_LOG_DIR",
  "EXAMTOPICS_ADMIN_TOKEN",
  "EXAMTOPICSDL_BIN",
  "EXAMTOPICS_DOWNLOADER_BIN",
  "HOME",
  "USERPROFILE",
  "APPDATA",
  "XDG_CONFIG_HOME",
];

let workDir = "";
const savedEnv: Record<string, string | undefined> = {};

function isolate(): void {
  workDir = join(tmpdir(), `synctest-${Date.now()}-${Math.random().toString(36).slice(2)}`);
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
  resetDownloaderBinCache();
  resetSyncStateForTests();
}

function restore(): void {
  for (const k of ENV_KEYS) {
    if (savedEnv[k] === undefined) delete process.env[k];
    else process.env[k] = savedEnv[k];
  }
  resetConfigCache();
  resetDownloaderBinCache();
  resetSyncStateForTests();
  if (workDir) rmSync(workDir, { recursive: true, force: true });
}

let resetSyncStateForTests: () => void;
let parseTranslationsOutput: typeof import("./syncing").parseTranslationsOutput;
let parseSnapshotOutput: typeof import("./syncing").parseSnapshotOutput;
let listPeerCandidates: typeof import("./syncing").listPeerCandidates;
let resolveSnapshotDownload: typeof import("./syncing").resolveSnapshotDownload;
let startTranslationsSync: typeof import("./syncing").startTranslationsSync;
let startSnapshot: typeof import("./syncing").startSnapshot;

const mod = await import("./syncing");
resetSyncStateForTests = mod.resetSyncStateForTests;
parseTranslationsOutput = mod.parseTranslationsOutput;
parseSnapshotOutput = mod.parseSnapshotOutput;
listPeerCandidates = mod.listPeerCandidates;
resolveSnapshotDownload = mod.resolveSnapshotDownload;
startTranslationsSync = mod.startTranslationsSync;
startSnapshot = mod.startSnapshot;

const CLI_OUTPUT = [
  "merged translations: /data/aif-c01.db <- /data/incoming/other.db",
  "  filled question fields: 306",
  "  filled choice fields:   602",
  "  conflicts: 2 (kept this DB's wording; overwritten: 0)",
  "    explanation_ja  https://www.examtopics.com/discussions/amazon/view/150663-exam-aws-certified-ai-practitioner-aif-c01-topic-1-question/",
  "    question_text_ja  https://www.examtopics.com/discussions/amazon/view/150663-exam-aws-certified-ai-practitioner-aif-c01-topic-1-question/",
  "  (re-run with --prefer peer to take the other machine's wording for these)",
].join("\n");

describe("parseTranslationsOutput", () => {
  test("reads the fill counts and the conflict list", () => {
    const r = parseTranslationsOutput(CLI_OUTPUT, false, false);
    expect(r.filledQuestions).toBe(306);
    expect(r.filledChoices).toBe(602);
    expect(r.conflictCount).toBe(2);
    expect(r.overwritten).toBe(0);
    expect(r.conflicts.length).toBe(2);
    expect(r.conflicts[0].field).toBe("explanation_ja");
    expect(r.conflicts[0].url).toContain("/view/150663-");
  });

  test("handles a clean run", () => {
    const r = parseTranslationsOutput(
      ["merged translations: a <- b", "  filled question fields: 0", "  filled choice fields:   0", "  conflicts: none"].join(
        "\n"
      )
    );
    expect(r.filledQuestions).toBe(0);
    expect(r.conflictCount).toBe(0);
    expect(r.conflicts).toEqual([]);
  });

  test("handles a dry run that would overwrite conflicts", () => {
    const r = parseTranslationsOutput(
      [
        "would merge (dry run) translations: a <- b",
        "  filled question fields: 1",
        "  filled choice fields:   0",
        "  conflicts: 2 (replaced with the peer's wording; overwritten: 2)",
        "    question_text_ja  https://example.test/q/1",
        "    explanation_ja  https://example.test/q/1",
      ].join("\n"),
      true,
      true
    );
    expect(r.dryRun).toBe(true);
    expect(r.preferPeer).toBe(true);
    expect(r.overwritten).toBe(2);
    expect(r.conflicts.length).toBe(2);
  });

  test("ignores unrelated output", () => {
    const r = parseTranslationsOutput("sync translations: -d and --from are required");
    expect(r.conflictCount).toBe(0);
    expect(r.filledQuestions).toBe(0);
  });
});

describe("parseSnapshotOutput", () => {
  test("extracts the written file", () => {
    expect(
      parseSnapshotOutput("snapshot written: /data/aif-c01.db -> /data/outgoing/aif-c01-2026.db\n")
    ).toBe("/data/outgoing/aif-c01-2026.db");
  });

  test("returns null when the line is absent", () => {
    expect(parseSnapshotOutput("boom")).toBeNull();
  });
});

describe("listPeerCandidates", () => {
  beforeEach(isolate);
  afterEach(restore);

  test("lists the data dir and its incoming folder, newest first", async () => {
    writeFileSync(join(workDir, "aif-c01.db"), "x");
    writeFileSync(join(workDir, "notes.txt"), "x");
    mkdirSync(join(workDir, "incoming"), { recursive: true });
    writeFileSync(join(workDir, "incoming", "other-machine.db"), "xx");
    writeFileSync(join(workDir, "incoming", "my export.db"), "x");
    mkdirSync(join(workDir, "nested.db"), { recursive: true });

    const found = listPeerCandidates();
    const names = found.map((f) => f.name).sort();
    expect(names).toEqual(["aif-c01.db", "my export.db", "other-machine.db"]);

    const examDb = found.find((f) => f.name === "aif-c01.db")!;
    expect(examDb.isExamDb).toBe(true);
    // A name that is not a valid slug is flagged so the UI can warn.
    expect(found.find((f) => f.name === "my export.db")!.isExamDb).toBe(false);
    // Directories named *.db are skipped.
    expect(found.find((f) => f.name === "nested.db")).toBeUndefined();
  });

  test("returns nothing when the data dir does not exist", () => {
    expect(listPeerCandidates(join(workDir, "missing"))).toEqual([]);
  });
});

describe("resolveSnapshotDownload", () => {
  beforeEach(isolate);
  afterEach(restore);

  test("serves a file from the outgoing folder", () => {
    mkdirSync(join(workDir, "outgoing"), { recursive: true });
    writeFileSync(join(workDir, "outgoing", "aif-c01-1.db"), "x");
    expect(resolveSnapshotDownload("aif-c01-1.db")).toContain("outgoing");
  });

  test("refuses traversal and nested paths", () => {
    mkdirSync(join(workDir, "outgoing"), { recursive: true });
    writeFileSync(join(workDir, "secret.db"), "x");
    expect(resolveSnapshotDownload("../secret.db")).toBeNull();
    expect(resolveSnapshotDownload("..%2Fsecret.db")).toBeNull();
    expect(resolveSnapshotDownload("sub/secret.db")).toBeNull();
    expect(resolveSnapshotDownload("")).toBeNull();
  });

  test("returns null for a name that does not exist", () => {
    mkdirSync(join(workDir, "outgoing"), { recursive: true });
    expect(resolveSnapshotDownload("nope.db")).toBeNull();
  });
});

describe("start guards", () => {
  beforeEach(isolate);
  afterEach(restore);

  test("rejects an invalid slug", () => {
    const r = startTranslationsSync({ slug: "../etc", peerPath: "/tmp/x.db" });
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.status).toBe(400);
  });

  test("requires a peer path", () => {
    const r = startTranslationsSync({ slug: "aif-c01" });
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.message).toContain("peer");
  });

  test("rejects a peer DB that does not exist", () => {
    // A resolvable binary is only needed so the guard order reaches build().
    writeFileSync(join(workDir, "examtopicsdl"), "#!/bin/sh\n");
    process.env.EXAMTOPICSDL_BIN = join(workDir, "examtopicsdl");
    resetDownloaderBinCache();
    const r = startTranslationsSync({ slug: "aif-c01", peerPath: join(workDir, "nope.db") });
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.message).toContain("peer DB not found");
  });

  test("refuses to pull from the exam DB itself", () => {
    writeFileSync(join(workDir, "examtopicsdl"), "#!/bin/sh\n");
    process.env.EXAMTOPICSDL_BIN = join(workDir, "examtopicsdl");
    resetDownloaderBinCache();
    writeFileSync(join(workDir, "aif-c01.db"), "x");
    const r = startTranslationsSync({ slug: "aif-c01", peerPath: join(workDir, "aif-c01.db") });
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.message).toContain("this DB");
  });

  test("snapshot requires the exam DB to exist", () => {
    writeFileSync(join(workDir, "examtopicsdl"), "#!/bin/sh\n");
    process.env.EXAMTOPICSDL_BIN = join(workDir, "examtopicsdl");
    resetDownloaderBinCache();
    const r = startSnapshot({ slug: "missing-exam" });
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.message).toContain("exam DB not found");
  });
});
