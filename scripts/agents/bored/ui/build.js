import * as esbuild from 'esbuild';
import * as fs from 'fs';

async function build() {
  const startTime = Date.now();

  if (!fs.existsSync('dist')) {
    fs.mkdirSync('dist');
  }

  // Build @pierre/diffs worker (IIFE for web worker context)
  console.log('Building diffs worker...');
  await esbuild.build({
    entryPoints: ['src/diffs-worker.ts'],
    bundle: true,
    outfile: 'dist/diffs-worker.js',
    format: 'iife',
    minify: true,
    sourcemap: true,
  });

  // Build main app
  console.log('Building main app...');
  await esbuild.build({
    entryPoints: ['src/index.tsx'],
    bundle: true,
    outfile: 'dist/main.js',
    format: 'esm',
    minify: true,
    sourcemap: true,
  });

  // Copy static files
  fs.copyFileSync('src/index.html', 'dist/index.html');
  fs.copyFileSync('src/styles.css', 'dist/styles.css');

  const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
  console.log(`Built in ${elapsed}s`);
}

build().catch((err) => {
  console.error('Build failed:', err);
  process.exit(1);
});
