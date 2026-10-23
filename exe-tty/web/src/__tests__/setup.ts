// Vitest setup file.

import { afterEach } from "vitest";
import { config } from "@vue/test-utils";
import type { VueWrapper } from "@vue/test-utils";

// Unmount every component a test mounted once the test ends. Vitest tears
// down happy-dom (deleting `window` et al.) after the file's last test, but
// Node timers keep running until the worker exits; a still-mounted component
// whose debounce/poll timer then fires re-renders into a missing DOM and the
// run fails with an unhandled "window is not defined" (seen in CI from
// PrimeVue's v-tooltip `updated` hook). Unmounting disposes each component's
// effect scope, so onScopeDispose/onUnmounted cleanup runs and reactive
// writes from a late callback no longer re-render anything.
//
// Not @vue/test-utils' enableAutoUnmount: that unmounts unconditionally, and
// a second app.unmount() re-runs the app's unmount path (Vue never resets
// its mounted flag), which many tests here would trigger since they
// wrapper.unmount() mid-test on purpose to assert cleanup. Skip those via
// exists(). Test-file afterEach hooks run before this one (vitest's default
// sequence.hooks: "stack"), so tests can still inspect a mounted wrapper in
// their own teardown.
//
// Unmount hooks may themselves defer DOM work by a tick (PrimeVue's tooltip
// `unmounted` does setTimeout(remove, 0), and `remove` reads `window`), so
// wait one real macrotask before returning. Captured before any test can
// install fake timers, so a test that forgets vi.useRealTimers() can't hang
// this hook.
const realSetTimeout = globalThis.setTimeout;
const mounted: VueWrapper[] = [];
config.plugins.VueWrapper.install((wrapper) => {
  // Only root wrappers (from mount()) own an app; findComponent() wrappers
  // don't and can't be unmounted.
  if ((wrapper as unknown as { __app: unknown }).__app) mounted.push(wrapper);
  return {};
});
afterEach(async () => {
  const errors: unknown[] = [];
  for (const wrapper of mounted.splice(0)) {
    try {
      if (wrapper.exists()) wrapper.unmount();
    } catch (err) {
      // Keep unmounting the rest; a broken unmount path is still a failure.
      errors.push(err);
    }
  }
  await new Promise((resolve) => realSetTimeout(resolve, 0));
  if (errors.length) throw errors[0];
});

// Node.js >= 22 ships a built-in localStorage that is a plain object without
// the full Web Storage API (no getItem, setItem, removeItem, clear, etc.).
// When happy-dom's populateGlobal sees that "localStorage" already exists on
// globalThis, it skips overriding it, leaving tests with a broken storage
// implementation. We fix this by deleting the broken built-in before
// happy-dom sets up the environment—but the setup file runs *after*
// populateGlobal, so instead we provide a spec-compliant shim here that
// survives the entire test run.

function makeStorage(): Storage {
  let store: Record<string, string> = {};
  return {
    get length() {
      return Object.keys(store).length;
    },
    key(index: number): string | null {
      return Object.keys(store)[index] ?? null;
    },
    getItem(key: string): string | null {
      return key in store ? store[key] : null;
    },
    setItem(key: string, value: string): void {
      store[key] = String(value);
    },
    removeItem(key: string): void {
      delete store[key];
    },
    clear(): void {
      store = {};
    },
  };
}

// Only patch if the existing localStorage is broken (missing clear).
if (typeof globalThis.localStorage?.clear !== "function") {
  Object.defineProperty(globalThis, "localStorage", {
    value: makeStorage(),
    writable: true,
    configurable: true,
  });
}
if (typeof globalThis.sessionStorage?.clear !== "function") {
  Object.defineProperty(globalThis, "sessionStorage", {
    value: makeStorage(),
    writable: true,
    configurable: true,
  });
}
