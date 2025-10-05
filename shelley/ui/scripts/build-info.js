#!/usr/bin/env node
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// Get the absolute path to the src directory
const srcDir = path.resolve(__dirname, '..', 'src');

const buildInfo = {
  timestamp: Date.now(),
  date: new Date().toISOString(),
  srcDir: srcDir
};

fs.writeFileSync(
  path.join(__dirname, '..', 'dist', 'build-info.json'),
  JSON.stringify(buildInfo, null, 2)
);

console.log('Build info written:', buildInfo.date);
