import * as esbuild from 'esbuild';
import * as fs from 'fs';
import * as zlib from 'zlib';
import * as crypto from 'crypto';

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
    // Get the absolute path to the src directory for staleness checking
    const srcDir = new URL('../src', import.meta.url).pathname;
    const buildInfo = {
      timestamp: Date.now(),
      date: new Date().toISOString(),
      srcDir: srcDir,
    };
    fs.writeFileSync('dist/build-info.json', JSON.stringify(buildInfo, null, 2));

    console.log('Build complete!');

    // Generate gzip versions of large files and remove originals to reduce binary size
    // The server will decompress on-the-fly for the rare clients that don't support gzip
    console.log('\nGenerating gzip compressed files...');
    const filesToCompress = ['monaco-editor.js', 'editor.worker.js', 'main.js', 'monaco-editor.css', 'styles.css'];
    const checksums = {};
    
    for (const file of filesToCompress) {
      const inputPath = `dist/${file}`;
      const outputPath = `dist/${file}.gz`;
      if (fs.existsSync(inputPath)) {
        const input = fs.readFileSync(inputPath);
        const compressed = zlib.gzipSync(input, { level: 9 });
        fs.writeFileSync(outputPath, compressed);
        
        // Compute SHA256 of the compressed content for ETag
        const hash = crypto.createHash('sha256').update(compressed).digest('hex').slice(0, 16);
        checksums[file] = hash;
        
        const origKb = (input.length / 1024).toFixed(1);
        const gzKb = (compressed.length / 1024).toFixed(1);
        const ratio = ((compressed.length / input.length) * 100).toFixed(0);
        console.log(`  ${file}: ${origKb} KB -> ${gzKb} KB gzip (${ratio}%) [${hash}]`);
        
        // Remove original to save space in embedded binary
        fs.unlinkSync(inputPath);
      }
    }
    
    // Write checksums for ETag support
    fs.writeFileSync('dist/checksums.json', JSON.stringify(checksums, null, 2));
    console.log('\nChecksums written to dist/checksums.json');

    console.log('\nOther files:');
    const otherFiles = fs.readdirSync('dist').filter(f => 
      (f.endsWith('.ttf') || f.endsWith('.map')) && !f.endsWith('.gz')
    );
    for (const file of otherFiles.sort()) {
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
