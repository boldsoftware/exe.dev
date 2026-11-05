// Build configuration shared by every app that bundles the exe-tty terminal.

import { resolve } from "node:path";

// ghostty-web is built from source, with our patches, by ../ghostty-web/build.sh
// into ./ghostty-web (gitignored): the library's TypeScript, which the app's
// Vite build compiles like its own code, and ghostty-vt.wasm, which
// terminalRenderer.ts imports as a hashed asset URL. These aliases point the
// package name at that output.
export function ghosttyWebAliases() {
  const dir = resolve(__dirname, "ghostty-web");
  return [
    { find: /^ghostty-web\/ghostty-vt\.wasm/, replacement: resolve(dir, "ghostty-vt.wasm") },
    { find: /^ghostty-web$/, replacement: resolve(dir, "lib/index.ts") },
  ];
}
