import { Hono } from "hono";
import type { HtmlEscapedString } from "hono/utils/html";

type Renderable = HtmlEscapedString | Promise<HtmlEscapedString>;

/**
 * Render a JSX element to an HTML string by mounting it on a throwaway Hono
 * app and exercising the actual SSR path. Avoids relying on JSXNode internals.
 */
export async function renderToString(node: Renderable): Promise<string> {
  const app = new Hono();
  app.get("/", (c) => c.html(node));
  const res = await app.request("/");
  return res.text();
}
