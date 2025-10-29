#!/usr/bin/env node

// Test server script for Playwright tests
const { spawn } = require('child_process');
const fs = require('fs');
const path = require('path');
const { mkdtempSync } = require('fs');
const { tmpdir } = require('os');
const net = require('net');

// Function to find an available port starting from a base port
function getAvailablePort(startPort = 9001) {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.unref();
    server.on('error', () => {
      // Try next port
      resolve(getAvailablePort(startPort + 1));
    });
    server.listen(startPort, () => {
      const port = server.address().port;
      server.close(() => {
        resolve(port);
      });
    });
  });
}

// Create a temporary directory for this test run
const tempDir = mkdtempSync(path.join(tmpdir(), 'shelley-e2e-'));
const testDb = path.join(tempDir, 'test.db');
const testDbShm = testDb + '-shm';
const testDbWal = testDb + '-wal';

console.log(`Using temporary database: ${testDb}`);

// Get an available port and start the server
getAvailablePort().then(port => {
  console.log(`Starting test server on port ${port}`);
  
  // Start Shelley server with test configuration
  const serverProcess = spawn('go', [
    'run', './cmd/shelley',
    '--model', 'predictable',
    '--predictable-only',
    '--db', testDb,
    'serve',
    '--port', port.toString()
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
      // Remove the entire temporary directory and all its contents
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
}).catch(error => {
  console.error('Failed to get available port:', error);
  process.exit(1);
});
