/**
 * Connector smoke test. Skipped unless EXE_VM_HOST is set.
 *
 * Locally:  EXE_VM_HOST=terminus.exe.xyz pnpm test
 * In CI:    no EXE_VM_HOST → skipped (typecheck still runs)
 *
 * Tests "does the connector work" — exercises each SandboxApi method
 * against a real exe.dev VM. Does not test Flue itself.
 */
import assert from "node:assert/strict";
import { exedev } from "../exedev";

const host = process.env.EXE_VM_HOST;

if (!host) {
  console.log("SKIP: EXE_VM_HOST not set");
  process.exit(0);
}

const factory = exedev({ host, cleanup: true });
const env = await factory.createSessionEnv({ id: "smoke-test" });

let failures = 0;
async function t(name: string, fn: () => Promise<void>) {
  try {
    await fn();
    console.log(`  ok  ${name}`);
  } catch (err) {
    failures++;
    console.log(`  FAIL ${name}`);
    console.error(err);
  }
}

const probeDir = `/tmp/flue-exedev-smoke-${Date.now()}`;
const probeFile = `${probeDir}/hello.txt`;
const probeBody = `hello from flue smoke at ${new Date().toISOString()}\n`;

console.log(`exedev connector smoke against ${host}`);

await t("exec: whoami returns a non-empty user", async () => {
  const r = await env.exec("whoami");
  assert.equal(r.exitCode, 0);
  assert.ok(r.stdout.trim().length > 0, "expected non-empty whoami stdout");
});

await t("exec: cwd option is honored", async () => {
  const r = await env.exec("pwd", { cwd: "/tmp" });
  assert.equal(r.exitCode, 0);
  assert.equal(r.stdout.trim(), "/tmp");
});

await t("exec: env option is honored", async () => {
  const r = await env.exec('echo "$FLUE_SMOKE"', {
    env: { FLUE_SMOKE: "wired" },
  });
  assert.equal(r.stdout.trim(), "wired");
});

await t("exec: non-zero exit propagates", async () => {
  const r = await env.exec("exit 7");
  assert.equal(r.exitCode, 7);
});

await t("mkdir: recursive creates nested dirs", async () => {
  await env.mkdir(probeDir, { recursive: true });
  assert.equal(await env.exists(probeDir), true);
});

await t("writeFile + readFile round-trip a string", async () => {
  await env.writeFile(probeFile, probeBody);
  const got = await env.readFile(probeFile);
  assert.equal(got, probeBody);
});

await t("writeFile + readFileBuffer round-trip bytes", async () => {
  const bytes = new Uint8Array([0, 1, 2, 3, 255, 254, 253]);
  const bytePath = `${probeDir}/bytes.bin`;
  await env.writeFile(bytePath, bytes);
  const got = await env.readFileBuffer(bytePath);
  assert.deepEqual(Array.from(got), Array.from(bytes));
});

await t("stat: reports file metadata", async () => {
  const s = await env.stat(probeFile);
  assert.equal(s.isFile, true);
  assert.equal(s.isDirectory, false);
  assert.equal(s.size, Buffer.byteLength(probeBody));
  assert.ok(s.mtime instanceof Date);
});

await t("readdir: lists directory entries", async () => {
  const entries = await env.readdir(probeDir);
  assert.ok(
    entries.includes("hello.txt"),
    `expected hello.txt in ${entries.join(",")}`,
  );
});

await t("exists: false for missing path", async () => {
  assert.equal(await env.exists(`${probeDir}/does-not-exist`), false);
});

await t("rm: recursive+force removes the probe dir", async () => {
  await env.rm(probeDir, { recursive: true, force: true });
  assert.equal(await env.exists(probeDir), false);
});

await env.cleanup();

if (failures > 0) {
  console.log(`\n${failures} failure(s)`);
  process.exit(1);
}
console.log("\nall smoke checks passed");
