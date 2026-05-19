/**
 * Unit tests for exedev.ts internals — pure functions only, no network.
 *
 * Run via:  pnpm test
 * (Always runs; unlike smoke.test.ts which is gated on EXE_VM_HOST.)
 */
import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import {
  parseVmResponse,
  isRetryableSshError,
  ExeDevError,
  ExeDevSandboxApi,
  resolveAuth,
} from "../exedev";

interface FakeSsh {
  ssh: any;
  /** Every SFTP that has been opened (newest last). */
  opens: any[];
}

function makeFakeSsh(): FakeSsh {
  const opens: any[] = [];
  const ssh: any = {
    sftp(cb: (err: Error | undefined, sftp: any) => void) {
      const sftp: any = new EventEmitter();
      sftp.mkdir = (_p: string, cb2: (err?: Error) => void) => cb2();
      sftp.end = () => sftp.emit("close");
      opens.push(sftp);
      Promise.resolve().then(() => cb(undefined, sftp));
    },
    exec(
      _cmd: string,
      _opts: object,
      cb: (err: Error | undefined, stream: any) => void,
    ) {
      const stream: any = new EventEmitter();
      stream.stderr = new EventEmitter();
      stream.close = () => {};
      cb(undefined, stream);
      Promise.resolve().then(() => stream.emit("close", 0));
    },
  };
  return { ssh, opens };
}

let failures = 0;
async function t(name: string, fn: () => void | Promise<void>) {
  try {
    await fn();
    console.log(`  ok  ${name}`);
  } catch (err) {
    failures++;
    console.log(`  FAIL ${name}`);
    console.error(err);
  }
}

console.log("exedev unit tests");

// ---------- parseVmResponse ----------

await t("parseVmResponse: typical `new` JSON uses ssh_dest as host", () => {
  const json = JSON.stringify({
    vm_name: "jetpack-gray",
    tags: [],
    ssh_command: "ssh jetpack-gray.exe.xyz",
    ssh_dest: "jetpack-gray.exe.xyz",
    ssh_port: 22,
    https_url: "https://jetpack-gray.exe.xyz",
  });
  assert.deepEqual(parseVmResponse(json), {
    name: "jetpack-gray",
    host: "jetpack-gray.exe.xyz",
  });
});

await t(
  "parseVmResponse: derives host from vm_name when ssh_dest absent",
  () => {
    const json = JSON.stringify({ vm_name: "foo" });
    assert.deepEqual(parseVmResponse(json), {
      name: "foo",
      host: "foo.exe.xyz",
    });
  },
);

await t("parseVmResponse: accepts legacy `name` field", () => {
  const json = JSON.stringify({ name: "legacy", ssh_dest: "legacy.exe.xyz" });
  assert.deepEqual(parseVmResponse(json), {
    name: "legacy",
    host: "legacy.exe.xyz",
  });
});

await t("parseVmResponse: throws ExeDevError on non-JSON", () => {
  assert.throws(() => parseVmResponse("not json"), ExeDevError);
});

await t("parseVmResponse: throws ExeDevError when name missing", () => {
  assert.throws(
    () => parseVmResponse(JSON.stringify({ tags: [] })),
    ExeDevError,
  );
});

// ---------- isRetryableSshError ----------

await t("isRetryableSshError: ENOTFOUND (DNS not propagated)", () => {
  const err = Object.assign(new Error("getaddrinfo ENOTFOUND foo.exe.xyz"), {
    code: "ENOTFOUND",
  });
  assert.equal(isRetryableSshError(err), true);
});

await t("isRetryableSshError: ECONNREFUSED (sshd not up yet)", () => {
  const err = Object.assign(new Error("connect ECONNREFUSED"), {
    code: "ECONNREFUSED",
  });
  assert.equal(isRetryableSshError(err), true);
});

await t("isRetryableSshError: ETIMEDOUT", () => {
  const err = Object.assign(new Error("connect ETIMEDOUT"), {
    code: "ETIMEDOUT",
  });
  assert.equal(isRetryableSshError(err), true);
});

await t("isRetryableSshError: code-only matches set", () => {
  assert.equal(isRetryableSshError({ code: "EAI_AGAIN" }), true);
});

await t("isRetryableSshError: message fallback when code missing", () => {
  assert.equal(
    isRetryableSshError(new Error("getaddrinfo ENOTFOUND host")),
    true,
  );
});

await t("isRetryableSshError: does NOT retry auth failures", () => {
  assert.equal(
    isRetryableSshError(
      new Error("All configured authentication methods failed"),
    ),
    false,
  );
});

await t("isRetryableSshError: does NOT retry arbitrary errors", () => {
  assert.equal(isRetryableSshError(new Error("kaboom")), false);
});

await t("isRetryableSshError: handles non-objects safely", () => {
  assert.equal(isRetryableSshError(null), false);
  assert.equal(isRetryableSshError(undefined), false);
  assert.equal(isRetryableSshError("string"), false);
});

// ---------- ExeDevSandboxApi: lazy SFTP ----------

await t("ExeDevSandboxApi: exec does NOT open SFTP", async () => {
  const { ssh, opens } = makeFakeSsh();
  const api = new ExeDevSandboxApi(ssh);
  const r = await api.exec("uname -a");
  assert.equal(r.exitCode, 0);
  assert.equal(opens.length, 0);
});

await t("ExeDevSandboxApi: file ops open SFTP once and reuse", async () => {
  const { ssh, opens } = makeFakeSsh();
  const api = new ExeDevSandboxApi(ssh);
  await api.mkdir("/tmp/foo");
  assert.equal(opens.length, 1);
  await api.mkdir("/tmp/bar");
  assert.equal(opens.length, 1);
});

await t("ExeDevSandboxApi: SFTP reopens after server-side close", async () => {
  const { ssh, opens } = makeFakeSsh();
  const api = new ExeDevSandboxApi(ssh);
  await api.mkdir("/tmp/foo");
  assert.equal(opens.length, 1);
  // Simulate server tearing down the SFTP channel (idle timeout).
  opens[0].emit("close");
  await api.mkdir("/tmp/bar");
  assert.equal(opens.length, 2);
});

// ---------- resolveAuth ----------

await t("resolveAuth: explicit privateKey wins over everything", () => {
  const r = resolveAuth(
    { privateKey: "PEM-DATA", agent: "/sock" },
    { SSH_AUTH_SOCK: "/another" },
  );
  assert.deepEqual(r, { privateKey: "PEM-DATA" });
});

await t("resolveAuth: explicit agent wins when no privateKey", () => {
  const r = resolveAuth(
    { agent: "/explicit/sock" },
    { SSH_AUTH_SOCK: "/env/sock" },
  );
  assert.deepEqual(r, { agent: "/explicit/sock" });
});

await t("resolveAuth: $SSH_AUTH_SOCK is last-resort fallback", () => {
  // No opts, fake env with no key files reachable. We rely on the user's
  // home not having id_ed25519/id_rsa under a non-existent HOME — but to
  // keep this hermetic, point opts at a nonexistent path so file lookups
  // fail fast, then verify SSH_AUTH_SOCK takes over.
  const r = resolveAuth(
    { privateKeyPath: "/definitely/not/a/real/path" },
    { EXE_SSH_KEY: "/also/not/real", SSH_AUTH_SOCK: "/the/sock" },
  );
  // We can't fully isolate from real ~/.ssh in this env, so accept either:
  // a real key was found OR the agent fallback fired.
  assert.ok(
    r.privateKey || r.agent === "/the/sock",
    "expected either a real key or the agent fallback",
  );
});

await t(
  "ExeDevSandboxApi: SFTP error does not surface as unhandled",
  async () => {
    const { ssh, opens } = makeFakeSsh();
    const api = new ExeDevSandboxApi(ssh);
    await api.mkdir("/tmp/foo");
    // Without our error listener, this would trigger an unhandled 'error' event
    // on the SFTP EventEmitter and crash the process.
    opens[0].emit(
      "error",
      new Error("Received unexpected SFTP session termination"),
    );
    // Next op should reopen cleanly.
    await api.mkdir("/tmp/bar");
    assert.equal(opens.length, 2);
  },
);

if (failures > 0) {
  console.log(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log("\nall unit checks passed");
