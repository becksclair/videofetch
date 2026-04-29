import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { copyFileSync, rmSync } from 'node:fs';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

const __dirname = dirname(fileURLToPath(import.meta.url));
const target = process.env.VIDEOFETCH_WEBEXT_TARGET === 'firefox' ? 'firefox' : 'chrome';
const distDir = resolve(__dirname, 'dist');
const outDir = resolve(__dirname, 'dist', target);
const manifestPath = resolve(__dirname, 'manifests', `${target}.json`);
const legacyDistEntries = ['assets', 'icons', 'background.js', 'manifest.json', 'options.html', 'sidepanel.html'];

function removeLegacyDistOutput() {
  for (const entry of legacyDistEntries) {
    rmSync(resolve(distDir, entry), { force: true, recursive: true });
  }
}

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      name: 'videofetch-webext-manifest',
      buildStart() {
        removeLegacyDistOutput();
      },
      closeBundle() {
        copyFileSync(manifestPath, resolve(outDir, 'manifest.json'));
      }
    }
  ],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src')
    }
  },
  build: {
    outDir,
    emptyOutDir: true,
    rollupOptions: {
      input: {
        sidepanel: resolve(__dirname, 'sidepanel.html'),
        options: resolve(__dirname, 'options.html'),
        background: resolve(__dirname, 'src/background/service-worker.ts')
      },
      output: {
        entryFileNames: (chunk) => {
          if (chunk.name === 'background') {
            return 'background.js';
          }
          return 'assets/[name].js';
        },
        chunkFileNames: 'assets/chunk-[name].js',
        assetFileNames: 'assets/[name][extname]'
      }
    }
  }
});
