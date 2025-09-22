#!/usr/bin/env node

// Test server script for Playwright tests
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');

// Clean up any existing test database
const testDb = path.join(__dirname, '../../test-e2e.db');
const testDbShm = testDb + '-shm';
const testDbWal = testDb + '-wal';

[testDb, testDbShm, testDbWal].forEach(file => {
  if (fs.existsSync(file)) {
    fs.unlinkSync(file);
    console.log(`Cleaned up ${path.basename(file)}`);
  }
});

// Start Shelley server with test configuration
const serverProcess = spawn('go', [
  'run', './cmd/shelley',
  '--model', 'predictable',
  '--db', 'test-e2e.db',
  'serve',
  '--port', '9000'
], {
  cwd: path.join(__dirname, '../..'),
  stdio: 'inherit'
});

// Handle cleanup on exit
process.on('SIGINT', () => {
  console.log('\nShutting down test server...');
  serverProcess.kill('SIGTERM');
  process.exit(0);
});

process.on('SIGTERM', () => {
  serverProcess.kill('SIGTERM');
  process.exit(0);
});

serverProcess.on('close', (code) => {
  console.log(`Test server exited with code ${code}`);
  process.exit(code);
});
