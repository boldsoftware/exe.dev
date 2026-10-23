import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import { writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { stripGhosttyInlineWasm } from "./vite-plugins";

export default defineConfig({
  plugins: [stripGhosttyInlineWasm(), vue()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    reportCompressedSize: false,
    target: "esnext",
    rollupOptions: {
      plugins: [
        {
          // dist/.gitkeep is committed so `go build` works before the web
          // UI has been built (the server embeds dist/).
          name: "restore-gitkeep",
          writeBundle() {
            writeFileSync(resolve(__dirname, "dist/.gitkeep"), "");
          },
        },
      ],
    },
  },
  test: {
    environment: "happy-dom",
    include: ["src/**/*.test.ts"],
    setupFiles: ["src/__tests__/setup.ts"],
  },
});
