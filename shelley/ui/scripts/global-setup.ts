// Playwright global setup: starts a shelley test server on a random port.
// The actual port is communicated via --port-file, then exported as
// PLAYWRIGHT_TEST_BASE_URL so every worker's baseURL fixture picks it up.

import { execSync, spawn, type ChildProcess } from 'child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'fs';
import { tmpdir } from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const shelleyDir = path.resolve(__dirname, '../..');
const binPath = path.join(shelleyDir, 'bin', 'shelley');

let serverProcess: ChildProcess | null = null;
let tempDir: string | null = null;

export default async function globalSetup() {
  // If pointing at an external server, skip everything.
  if (process.env.TEST_SERVER_URL) {
    process.env.PLAYWRIGHT_TEST_BASE_URL = process.env.TEST_SERVER_URL;
    return;
  }

  // Build shelley binary if it doesn't exist.
  if (!existsSync(binPath)) {
    console.log('Building shelley binary…');
    execSync('go build -o bin/shelley ./cmd/shelley', {
      cwd: shelleyDir,
      stdio: 'inherit',
    });
  }

  // Create temp dir for database and port file.
  tempDir = mkdtempSync(path.join(tmpdir(), 'shelley-e2e-'));
  const testDb = path.join(tempDir, 'test.db');
  const portFile = path.join(tempDir, 'port');

  console.log(`Starting shelley (db=${testDb}, port-file=${portFile})`);

  let earlyExit = false;
  let exitCode: number | null = null;

  serverProcess = spawn(binPath, [
    '--model', 'predictable',
    '--predictable-only',
    '--db', testDb,
    'serve',
    '--port', '0',
    '--port-file', portFile,
  ], {
    cwd: shelleyDir,
    stdio: 'inherit',
    env: {
      ...process.env,
      PREDICTABLE_DELAY_MS: process.env.PREDICTABLE_DELAY_MS || '400',
    },
  });

  serverProcess.on('exit', (code) => {
    earlyExit = true;
    exitCode = code;
  });

  // Wait for port file to appear (server has bound).
  const deadline = Date.now() + 30_000;
  while (!existsSync(portFile)) {
    if (earlyExit) {
      throw new Error(`Shelley server exited (code ${exitCode}) before writing port file`);
    }
    if (Date.now() > deadline) {
      throw new Error('Shelley server did not write port file within 30s');
    }
    await new Promise(r => setTimeout(r, 50));
  }

  const port = readFileSync(portFile, 'utf8').trim();
  const baseURL = `http://localhost:${port}`;
  console.log(`Shelley test server listening at ${baseURL}`);

  // Wait for the server to actually respond to HTTP.
  const httpDeadline = Date.now() + 30_000;
  let httpReady = false;
  while (Date.now() < httpDeadline) {
    if (earlyExit) {
      throw new Error(`Shelley server exited (code ${exitCode}) during startup`);
    }
    try {
      const res = await fetch(baseURL);
      if (res.ok) { httpReady = true; break; }
    } catch {
      // not ready yet
    }
    await new Promise(r => setTimeout(r, 100));
  }
  if (!httpReady) {
    throw new Error(`Shelley server at ${baseURL} never responded OK within 30s`);
  }

  // Playwright's built-in baseURL fixture reads this env var.
  process.env.PLAYWRIGHT_TEST_BASE_URL = baseURL;

  // Return teardown function.
  return async () => {
    if (serverProcess) {
      serverProcess.kill('SIGTERM');
      serverProcess = null;
    }
    if (tempDir) {
      rmSync(tempDir, { recursive: true, force: true });
      tempDir = null;
    }
  };
}
