#!/usr/bin/env node

// Test server script for Playwright tests
const { spawn, execSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const { mkdtempSync } = require('fs');
const { tmpdir } = require('os');

const PORT = 9001;

// Kill any stale process listening on our port. On self-hosted CI runners,
// a previous run may have left an orphaned shelley server (go run spawns a
// subprocess that can escape the runner's process-tree cleanup).
function killStaleProcessOnPort(port) {
  try {
    // lsof works on both macOS and Linux
    const output = execSync(
      `lsof -ti tcp:${port}`,
      { encoding: 'utf8', timeout: 5000 }
    ).trim();
    if (output) {
      const pids = output.split('\n').filter(Boolean);
      console.log(`Killing stale process(es) on port ${port}: ${pids.join(', ')}`);
      for (const pid of pids) {
        try {
          process.kill(parseInt(pid, 10), 'SIGKILL');
        } catch (e) {
          // Process may have already exited
        }
      }
      // Wait for the port to become available
      waitForPortFree(port);
    }
  } catch (e) {
    // lsof exits non-zero when nothing is listening -- that's fine
  }
}

// Poll until the port is free, up to 5 seconds.
function waitForPortFree(port) {
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    try {
      execSync(`lsof -ti tcp:${port}`, { encoding: 'utf8', timeout: 1000 });
      // Still in use, wait 100ms
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 100);
    } catch (e) {
      // lsof exited non-zero: port is free
      return;
    }
  }
  console.warn(`Warning: port ${port} still in use after 5s`);
}

killStaleProcessOnPort(PORT);

// Create a temporary directory for this test run
const tempDir = mkdtempSync(path.join(tmpdir(), 'shelley-e2e-'));
const testDb = path.join(tempDir, 'test.db');

console.log(`Using temporary database: ${testDb}`);
console.log(`Starting test server on port ${PORT}`);

// Start Shelley server with test configuration
const serverProcess = spawn('go', [
  'run', './cmd/shelley',
  '--model', 'predictable',
  '--predictable-only',
  '--db', testDb,
  'serve',
  '--port', PORT.toString()
], {
  cwd: path.join(__dirname, '../..'),
  stdio: 'inherit',
  env: {
    ...process.env,
    PREDICTABLE_DELAY_MS: process.env.PREDICTABLE_DELAY_MS || '400'
  }
});

// Cleanup function for temporary directory and database files
const cleanup = () => {
  try {
    fs.rmSync(tempDir, { recursive: true, force: true });
    console.log(`Cleaned up temporary directory: ${tempDir}`);
  } catch (error) {
    console.warn(`Failed to clean up temporary directory: ${error.message}`);
  }
};

// Handle cleanup on exit
process.on('SIGINT', () => {
  console.log('\nShutting down test server...');
  serverProcess.kill('SIGTERM');
  cleanup();
  process.exit(0);
});

process.on('SIGTERM', () => {
  serverProcess.kill('SIGTERM');
  cleanup();
  process.exit(0);
});

serverProcess.on('close', (code) => {
  console.log(`Test server exited with code ${code}`);
  cleanup();
  process.exit(code);
});
