// Build plugins shared by every app that bundles the exe-tty terminal.

// Strip ghostty-web's inlined base64 WASM data: URL from its bundled chunk.
//
// ghostty-web ships the ~424 KB WASM binary inlined as a
// `data:application/wasm;base64,...` string inside its ESM entry (so `init()`
// works with zero extra requests). We instead load the WASM from a hashed,
// browser-cacheable asset URL (see terminalRenderer.ts, which imports
// `ghostty-web/ghostty-vt.wasm?url` and calls Ghostty.load(url)). That leaves
// the giant base64 literal as dead weight bloating the JS chunk by ~565 KB.
//
// This plugin rewrites the literal to just its prefix in the built output.
// The data: URL is only the *first* WASM-load candidate; with it emptied the
// candidate simply fails and ghostty-web never uses it because we hand it a
// pre-loaded instance via Ghostty.load() anyway. Guarded to only fire when the
// exact base64 marker is present so an upstream change can't silently corrupt
// unrelated code.
export function stripGhosttyInlineWasm() {
  const marker = "data:application/wasm;base64,";
  return {
    name: "exe-tty-strip-ghostty-inline-wasm",
    apply: "build" as const,
    enforce: "post" as const,
    generateBundle(_opts: unknown, bundle: Record<string, { type: string; code?: string }>) {
      for (const chunk of Object.values(bundle)) {
        if (chunk.type !== "chunk" || !chunk.code || !chunk.code.includes(marker)) continue;
        chunk.code = chunk.code.replace(/data:application\/wasm;base64,[A-Za-z0-9+/=]+/g, marker);
      }
    },
  };
}
