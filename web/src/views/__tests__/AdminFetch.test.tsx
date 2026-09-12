import { describe, expect, test } from "bun:test";
import { AdminFetch, AdminLogin } from "../AdminFetch";
import { renderToString } from "./render";

describe("AdminFetch view", () => {
  test("renders a provider <select> and slug <input>", async () => {
    const html = await renderToString(
      <AdminFetch
        providers={["amazon", "microsoft"]}
        defaultProvider="amazon"
        providerLoadError={null}
        binPath="/tmp/examtopicsdl"
        binResolveError={null}
        job={null}
        loginEnabled={true}
        cookieAuthed={false}
      />
    );
    expect(html).toContain('action="/admin/fetch"');
    expect(html).toContain('name="provider"');
    expect(html).toContain('name="slug"');
    expect(html).toContain("amazon");
    expect(html).toContain("microsoft");
    // Default provider should be marked selected.
    expect(html).toMatch(/<option[^>]*value="amazon"[^>]*selected/);
  });

  test("hides the form and shows error when binary cannot be resolved", async () => {
    const html = await renderToString(
      <AdminFetch
        providers={[]}
        defaultProvider="amazon"
        providerLoadError={null}
        binPath={null}
        binResolveError="examtopicsdl binary not found. Tried (in order): ..."
        job={null}
        loginEnabled={true}
        cookieAuthed={false}
      />
    );
    expect(html).toContain("バイナリ解決エラー");
    expect(html).toContain("not found");
    // Form should be absent.
    expect(html).not.toContain('name="provider"');
  });

  test("renders the in-flight status block", async () => {
    const html = await renderToString(
      <AdminFetch
        providers={["amazon"]}
        defaultProvider="amazon"
        providerLoadError={null}
        binPath="/tmp/examtopicsdl"
        binResolveError={null}
        job={{
          provider: "amazon",
          slug: "saa-c03",
          startedAt: Date.parse("2026-05-06T10:00:00Z"),
          running: true,
          exitCode: null,
        }}
        loginEnabled={true}
        cookieAuthed={true}
      />
    );
    expect(html).toContain("running");
    expect(html).toContain("amazon");
    expect(html).toContain("saa-c03");
    // Submit button should be disabled while running.
    expect(html).toMatch(/data-role="admin-fetch-submit"[^>]*disabled/);
  });

  test("renders the SSE log container with the right id", async () => {
    const html = await renderToString(
      <AdminFetch
        providers={["amazon"]}
        defaultProvider="amazon"
        providerLoadError={null}
        binPath="/tmp/examtopicsdl"
        binResolveError={null}
        job={null}
        loginEnabled={true}
        cookieAuthed={false}
      />
    );
    expect(html).toMatch(/id="admin-fetch-log"/);
    expect(html).toContain("/admin/fetch/log");
  });

  test("inlines the admin-fetch client script", async () => {
    const html = await renderToString(
      <AdminFetch
        providers={["amazon"]}
        defaultProvider="amazon"
        providerLoadError={null}
        binPath="/tmp/examtopicsdl"
        binResolveError={null}
        job={null}
        loginEnabled={true}
        cookieAuthed={false}
      />
    );
    expect(html).toContain("EventSource");
    expect(html).toContain("/admin/fetch/log");
  });
});

describe("AdminLogin view", () => {
  test("renders the token input form", async () => {
    const html = await renderToString(<AdminLogin notice={null} />);
    expect(html).toContain('action="/admin/login"');
    expect(html).toContain('name="token"');
    expect(html).toContain("admin login");
  });

  test("surfaces an error notice when supplied", async () => {
    const html = await renderToString(
      <AdminLogin notice={{ kind: "error", text: "トークンが一致しません" }} />
    );
    expect(html).toContain("トークンが一致しません");
  });
});
