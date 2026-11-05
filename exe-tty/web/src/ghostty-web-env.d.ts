// ghostty-web's WASM loader probes for Bun at runtime. Upstream typechecks
// against bun-types; we only need the global to exist for the probe.
declare const Bun:
  | { file(path: string): { exists(): Promise<boolean>; arrayBuffer(): Promise<ArrayBuffer> } }
  | undefined;
