import * as esbuild from 'esbuild';
import * as fs from 'fs';

const isWatch = process.argv.includes('--watch');
const isProd = !isWatch;

async function build() {
  try {
    // Ensure dist directory exists
    if (!fs.existsSync('dist')) {
      fs.mkdirSync('dist');
    }

    // Build Monaco editor worker separately (IIFE format for web worker)
    console.log('Building Monaco editor worker...');
    await esbuild.build({
      entryPoints: ['node_modules/monaco-editor/esm/vs/editor/editor.worker.js'],
      bundle: true,
      outfile: 'dist/editor.worker.js',
      format: 'iife',
      minify: isProd,
      sourcemap: true,
    });

    // Build Monaco editor as a separate chunk (JS + CSS)
    console.log('Building Monaco editor bundle...');
    await esbuild.build({
      entryPoints: ['node_modules/monaco-editor/esm/vs/editor/editor.main.js'],
      bundle: true,
      outfile: 'dist/monaco-editor.js',
      format: 'esm',
      minify: isProd,
      sourcemap: true,
      loader: {
        '.ttf': 'file',
      },
    });

    // Build main app - exclude monaco-editor, we'll load it dynamically
    console.log('Building main application...');
    const result = await esbuild.build({
      entryPoints: ['src/main.tsx'],
      bundle: true,
      outfile: 'dist/main.js',
      format: 'esm',
      minify: isProd,
      sourcemap: true,
      metafile: true,
      external: ['monaco-editor', '/monaco-editor.js'],
    });

    // Copy static files
    fs.copyFileSync('src/index.html', 'dist/index.html');
    fs.copyFileSync('src/styles.css', 'dist/styles.css');

    // Write build info
    const buildInfo = { timestamp: new Date().toISOString() };
    fs.writeFileSync('dist/build-info.json', JSON.stringify(buildInfo, null, 2));

    console.log('Build complete!');

    // Show file sizes
    console.log('\nOutput files:');
    const files = fs.readdirSync('dist').filter(f => f.endsWith('.js') || f.endsWith('.css') || f.endsWith('.ttf'));
    for (const file of files.sort()) {
      const stats = fs.statSync(`dist/${file}`);
      const sizeKb = (stats.size / 1024).toFixed(1);
      console.log(`  ${file}: ${sizeKb} KB`);
    }
  } catch (error) {
    console.error('Build failed:', error);
    process.exit(1);
  }
}

build();
