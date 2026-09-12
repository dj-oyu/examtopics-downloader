// Module declarations for build-time text imports used by Bun's
// `with { type: "text" }` loader. SQL migrations (§2 notice 2) and the
// AGENTS.md prompt rules excerpt (§2 notice 7) are imported as raw
// strings so the compiled Bun binary can serve them without runtime
// filesystem access.
//
// For `.ts` source files imported as text (the client-script loader in
// src/client/loader.ts), TypeScript treats them as modules regardless
// of the import attribute, so loader.ts uses an inline @ts-expect-error
// instead — pattern-matching `*.ts` here would shadow legitimate TS
// imports project-wide.

declare module "*.sql" {
  const content: string;
  export default content;
}

declare module "*.md" {
  const content: string;
  export default content;
}
