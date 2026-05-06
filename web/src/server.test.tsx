import { describe, expect, test, beforeAll, beforeEach, afterEach } from "bun:test";
import { mkdirSync, rmSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

// Server admin route smoke tests. The data dir / admin token are
// pinned in module scope before server.tsx is imported, because
// db.ts captures loadConfig().dataDir at module init. The tests then
// adjust process.env.EXAMTOPICS_ADMIN_TOKEN per-case (the admin module
// reads it lazily inside getAdminToken()).
//
// The `examtopicsdl providers` round-trip is intentionally skipped —
// the admin module's tryLoadProviders() returns null when the binary
// cannot be located and the routes degrade gracefully (form is hidden,
// 503 message surfaces). Real provider parsing belongs in E2E.

const TEST_DIR = join(
  tmpdir(),
  `srvtest-${Date.now()}-${Math.random().toString(36).slice(2)}`
);

beforeAll(() => {
  mkdirSync(TEST_DIR, { recursive: true });
  process.env.EXAMTOPICS_DATA_DIR = TEST_DIR;
  // Force the binary lookup to fail by pointing at a non-existent
  // path; we don't need a real examtopicsdl for these route tests.
  process.env.EXAMTOPICSDL_BIN = join(TEST_DIR, "nope");
  // Ensure the cookie middleware's same-origin POST guard accepts our
  // synthetic requests. We pass the right Origin header per-case.
});

const ORIGIN = "http://localhost:3000";
const HOST = "localhost:3000";

async function getApp(): Promise<{ app: import("hono").Hono }> {
  // Import lazily after env is staged.
  const mod = await import("./server");
  return { app: mod.app as unknown as import("hono").Hono };
}

afterEach(async () => {
  delete process.env.EXAMTOPICS_ADMIN_TOKEN;
  const admin = await import("./admin");
  admin.resetFetchStateForTests();
  admin.resetDownloaderBinCache();
  admin.resetProvidersCache();
});

describe("GET /admin/fetch (token unset)", () => {
  test("returns 503 with the expected disabled body", async () => {
    const { app } = await getApp();
    const res = await app.request("/admin/fetch");
    expect(res.status).toBe(503);
    const body = await res.text();
    expect(body).toContain("admin disabled");
    expect(body).toContain("EXAMTOPICS_ADMIN_TOKEN");
  });
});

describe("GET /admin/fetch (token set, no auth)", () => {
  beforeEach(() => {
    process.env.EXAMTOPICS_ADMIN_TOKEN = "test-token";
  });
  test("renders the login form with 401", async () => {
    const { app } = await getApp();
    const res = await app.request("/admin/fetch");
    expect(res.status).toBe(401);
    const body = await res.text();
    expect(body).toContain("admin login");
  });
});

describe("GET /admin/fetch (Bearer auth)", () => {
  beforeEach(() => {
    process.env.EXAMTOPICS_ADMIN_TOKEN = "test-token";
  });
  test("renders the AdminFetch view", async () => {
    const { app } = await getApp();
    const res = await app.request("/admin/fetch", {
      headers: { authorization: "Bearer test-token" },
    });
    expect(res.status).toBe(200);
    const body = await res.text();
    expect(body).toContain("取得 (admin)");
    // Without a real examtopicsdl, providers fail and the form is
    // hidden — confirm the degraded message is visible.
    expect(body).toContain("プロバイダ");
  });

  test("rejects an incorrect Bearer token", async () => {
    const { app } = await getApp();
    const res = await app.request("/admin/fetch", {
      headers: { authorization: "Bearer wrong" },
    });
    // Wrong token → falls through to login UI on GET
    expect(res.status).toBe(401);
  });
});

describe("POST /admin/fetch (token set, wrong token)", () => {
  beforeEach(() => {
    process.env.EXAMTOPICS_ADMIN_TOKEN = "test-token";
  });
  test("returns 401 when bearer is wrong", async () => {
    const { app } = await getApp();
    const body = new URLSearchParams({ provider: "amazon", slug: "saa-c03" });
    const res = await app.request("/admin/fetch", {
      method: "POST",
      headers: {
        authorization: "Bearer not-it",
        origin: ORIGIN,
        host: HOST,
        "content-type": "application/x-www-form-urlencoded",
      },
      body,
    });
    expect(res.status).toBe(401);
  });

  test("returns 503 when admin token is unset entirely", async () => {
    delete process.env.EXAMTOPICS_ADMIN_TOKEN;
    const { app } = await getApp();
    const body = new URLSearchParams({ provider: "amazon", slug: "saa-c03" });
    const res = await app.request("/admin/fetch", {
      method: "POST",
      headers: {
        origin: ORIGIN,
        host: HOST,
        "content-type": "application/x-www-form-urlencoded",
      },
      body,
    });
    expect(res.status).toBe(503);
  });
});

describe("POST /admin/fetch validation", () => {
  beforeEach(() => {
    process.env.EXAMTOPICS_ADMIN_TOKEN = "test-token";
  });

  test("rejects an invalid slug with 400", async () => {
    const { app } = await getApp();
    // No real examtopicsdl, so the provider whitelist check would also
    // fail — but we hit slug validation first per the route order.
    const body = new URLSearchParams({
      provider: "amazon",
      slug: "../etc/passwd",
    });
    const res = await app.request("/admin/fetch", {
      method: "POST",
      headers: {
        authorization: "Bearer test-token",
        origin: ORIGIN,
        host: HOST,
        accept: "text/plain",
        "content-type": "application/x-www-form-urlencoded",
      },
      body,
    });
    expect(res.status).toBe(400);
  });

  test("rejects when provider whitelist cannot be loaded (no binary)", async () => {
    const { app } = await getApp();
    const body = new URLSearchParams({
      provider: "amazon",
      slug: "saa-c03",
    });
    const res = await app.request("/admin/fetch", {
      method: "POST",
      headers: {
        authorization: "Bearer test-token",
        origin: ORIGIN,
        host: HOST,
        accept: "text/plain",
        "content-type": "application/x-www-form-urlencoded",
      },
      body,
    });
    // examtopicsdl unreachable → 400 with the surfacing message.
    expect(res.status).toBe(400);
    const text = await res.text();
    expect(text.toLowerCase()).toMatch(/providers|binary|reach|not found/);
  });

  test("returns 409 when a job is already in flight", async () => {
    const admin = await import("./admin");
    admin.installFakeJobForTests({ provider: "amazon", slug: "saa-c03" });
    // Force a successful provider list so the in-flight check fires
    // before the binary-lookup branch.
    // (We can't easily inject the providers cache from here without
    // exposing more internals — instead we cover the in-flight path
    // by also setting EXAMTOPICSDL_BIN to a real, fake, executable.)
    // Approach: pre-seed providers via a synthetic shim binary that
    // prints "amazon\n".
    const shim = join(TEST_DIR, makeShimName());
    writeShim(shim, "amazon\nmicrosoft\n");
    process.env.EXAMTOPICSDL_BIN = shim;
    admin.resetDownloaderBinCache();
    admin.resetProvidersCache();

    const { app } = await getApp();
    const body = new URLSearchParams({
      provider: "amazon",
      slug: "saa-c03",
    });
    const res = await app.request("/admin/fetch", {
      method: "POST",
      headers: {
        authorization: "Bearer test-token",
        origin: ORIGIN,
        host: HOST,
        accept: "text/plain",
        "content-type": "application/x-www-form-urlencoded",
      },
      body,
    });
    expect(res.status).toBe(409);
    const text = await res.text();
    expect(text).toContain("in flight");
  });
});

describe("GET /admin/fetch/log SSE smoke", () => {
  beforeEach(() => {
    process.env.EXAMTOPICS_ADMIN_TOKEN = "test-token";
  });

  test("streams an event for a fake in-flight job", async () => {
    const admin = await import("./admin");
    admin.installFakeJobForTests({
      provider: "amazon",
      slug: "saa-c03",
      log: [{ stream: "stdout", text: "hello world", ts: Date.now() }],
    });

    const { app } = await getApp();
    const res = await app.request("/admin/fetch/log", {
      headers: { authorization: "Bearer test-token" },
    });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toContain("text/event-stream");

    // Read just enough to confirm the buffered line was replayed.
    const reader = res.body!.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    const deadline = Date.now() + 1500;
    while (Date.now() < deadline) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      if (buf.includes("hello world")) break;
    }
    try {
      await reader.cancel();
    } catch {}
    expect(buf).toContain("hello world");
  });
});

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

function makeShimName(): string {
  // Run a minimal script that prints provider names. On Windows we
  // need a .cmd; on POSIX a regular shell script.
  return process.platform === "win32" ? "providers-shim.cmd" : "providers-shim.sh";
}

function writeShim(path: string, providers: string): void {
  if (process.platform === "win32") {
    // .cmd: print each provider on its own line.
    const lines = providers.trim().split(/\r?\n/);
    const body =
      "@echo off\r\n" +
      lines.map((p) => `@echo ${p}`).join("\r\n") +
      "\r\n";
    writeFileSync(path, body);
  } else {
    const body =
      "#!/bin/sh\n" +
      providers
        .trim()
        .split(/\r?\n/)
        .map((p) => `echo "${p}"`)
        .join("\n") +
      "\n";
    writeFileSync(path, body, { mode: 0o755 });
  }
  if (!existsSync(path)) throw new Error(`shim not written: ${path}`);
}

// Cleanup the test data dir on the very last test exit.
process.on("exit", () => {
  try {
    rmSync(TEST_DIR, { recursive: true, force: true });
  } catch {}
});
