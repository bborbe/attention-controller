// Stops the binary global-setup started and removes its temp root.

import fs from "node:fs";

import { STATE_PATH, readState } from "./support";

export default async function globalTeardown(): Promise<void> {
  if (!fs.existsSync(STATE_PATH)) return;
  const state = readState();
  try {
    process.kill(-state.pid, "SIGKILL");
  } catch {
    try {
      process.kill(state.pid, "SIGKILL");
    } catch {
      /* already gone */
    }
  }
  try {
    fs.rmSync(state.root, { recursive: true, force: true });
  } catch {
    /* best effort */
  }
  try {
    fs.rmSync(STATE_PATH, { force: true });
  } catch {
    /* best effort */
  }
}
