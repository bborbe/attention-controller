import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    // /jump is the card's jump control (GET /jump/{itemID}), served by the store.
    proxy: { "/api": "http://127.0.0.1:18080", "/jump": "http://127.0.0.1:18080" },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/setupTests.ts"],
    // e2e/ holds Playwright specs; its test() API cannot run under Vitest.
    exclude: ["node_modules/**", "dist/**", "e2e/**"],
  },
});
