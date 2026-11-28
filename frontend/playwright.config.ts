import { defineConfig } from "@playwright/test";

// The React board's end-to-end suite. It drives the REAL binary (built and
// started by e2e/global-setup.ts) against a temp datadir, so every spec sees the
// production page, the production push/answer API and the production SSE stream.
//
// One worker: the specs share one store. Each spec seeds its own items and
// addresses them by id, and the two specs that need a whole-board view fulfil
// the board read in the browser rather than emptying a shared store.
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: [["list"]],
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",
  use: {
    headless: true,
    trace: "off",
    video: "off",
    screenshot: "off",
  },
});
