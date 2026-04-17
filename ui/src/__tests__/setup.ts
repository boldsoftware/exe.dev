// Vitest setup file.
//
// Node.js >= 22 ships a built-in localStorage that is a plain object without
// the full Web Storage API (no getItem, setItem, removeItem, clear, etc.).
// When happy-dom's populateGlobal sees that "localStorage" already exists on
// globalThis, it skips overriding it, leaving tests with a broken storage
// implementation. We fix this by deleting the broken built-in before
// happy-dom sets up the environment—but the setup file runs *after*
// populateGlobal, so instead we provide a spec-compliant shim here that
// survives the entire test run.

function makeStorage(): Storage {
  let store: Record<string, string> = {}
  return {
    get length() {
      return Object.keys(store).length
    },
    key(index: number): string | null {
      return Object.keys(store)[index] ?? null
    },
    getItem(key: string): string | null {
      return key in store ? store[key] : null
    },
    setItem(key: string, value: string): void {
      store[key] = String(value)
    },
    removeItem(key: string): void {
      delete store[key]
    },
    clear(): void {
      store = {}
    },
  }
}

// Only patch if the existing localStorage is broken (missing clear).
if (typeof globalThis.localStorage?.clear !== 'function') {
  Object.defineProperty(globalThis, 'localStorage', {
    value: makeStorage(),
    writable: true,
    configurable: true,
  })
}
if (typeof globalThis.sessionStorage?.clear !== 'function') {
  Object.defineProperty(globalThis, 'sessionStorage', {
    value: makeStorage(),
    writable: true,
    configurable: true,
  })
}
