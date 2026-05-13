import { defineConfig } from "vite";

export default defineConfig({
  build: {
    lib: {
      entry: "./src/index.ts",
      formats: ["es"],
      fileName: () => "mcp.js",
    },
    target: "es2022",
    minify: false,
    rollupOptions: {
      // No externals — the output is a self-contained ESM bundle loaded
      // dynamically by the Paca MCP server at runtime.
      external: [],
    },
  },
});
