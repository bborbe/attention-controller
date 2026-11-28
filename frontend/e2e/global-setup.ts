// Builds and starts the real binary, hermetically, for the whole Playwright run.
//
// It mirrors the Go e2e suite's setup (e2e/board_test.go BeforeSuite): a temp
// datadir, a temp session registry holding the fixture sessions, the legacy
// jump listener and the bearer-token listener disabled, and one spawn-ledger
// row per fixture session posted through the running binary's own endpoint.
//
// Everything the binary would otherwise read from the host is pointed at the
// temp root — the vault, the unforwarded-closers state, the statusline context
// dir, the session heartbeat store and the wrapper URL — so a run cannot touch
// real state. The read-aloud route is disabled server-side (`-tts-url ""`); the
// specs that exercise it intercept the request in the browser.

import { spawn, spawnSync } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";

import { FLEET_SESSION_ID, SESSION_ID, STATE_PATH, TAB_SESSION_ID } from "./support";

function frontendDir(): string {
  const cwd = process.cwd();
  if (fs.existsSync(path.join(cwd, "playwright.config.ts"))) return cwd;
  if (fs.existsSync(path.join(cwd, "frontend", "playwright.config.ts"))) {
    return path.join(cwd, "frontend");
  }
  throw new Error(`cannot locate frontend/ from cwd ${cwd}`);
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      srv.close(() => resolve(port));
    });
  });
}

async function waitHealthy(baseURL: string, deadlineMs = 40000): Promise<void> {
  const deadline = Date.now() + deadlineMs;
  let lastError = "no attempt";
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseURL}/healthz`);
      if (res.ok) return;
      lastError = `status ${res.status}`;
    } catch (err) {
      lastError = String(err);
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  throw new Error(`binary did not become healthy at ${baseURL}: ${lastError}`);
}

// waitReady polls /readiness until the binary has finished its init steps.
// /healthz answers 200 while the store is still opening, and every API route
// answers 503 {"status":"starting"} until then — seeding the ledger right after
// /healthz raced that window.
async function waitReady(baseURL: string, deadlineMs = 40000): Promise<void> {
  const deadline = Date.now() + deadlineMs;
  let lastError = "no attempt";
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseURL}/readiness`);
      if (res.ok) return;
      lastError = `status ${res.status}`;
    } catch (err) {
      lastError = String(err);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`binary did not become ready at ${baseURL}: ${lastError}`);
}

// startTTSStub stands in for the tts server, mirroring the Go suite's ttsStub
// (e2e/board_test.go): the board proxies its read-aloud control to POST /say
// and the stub answers with a message id. It lives in the runner process for
// the whole run; unref() keeps it from holding the process open.
function startTTSStub(): Promise<string> {
  const server = http.createServer((req, res) => {
    if (req.url !== "/say") {
      res.statusCode = 404;
      res.end();
      return;
    }
    req.resume();
    req.on("end", () => {
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ message_id: "stub-message-1" }));
    });
  });
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.unref();
      const addr = server.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      resolve(`http://127.0.0.1:${port}`);
    });
  });
}

function writeSessionRegistry(dir: string): void {
  fs.mkdirSync(dir, { recursive: true });
  // `nameSource: user` is load-bearing: only that source renders a session
  // name, and the name is what a navigation line would show.
  //
  // The live "Fleet Manager" entry is load-bearing too: a live Fleet Manager
  // covers every session with no live ancestor (pkg/worker-coverage.go), so the
  // e2e and e2e-tab cards are COVERED — hidden on the managers-only view — while
  // the Fleet Manager's own card has no relaying parent and is UNCOVERED.
  for (const [file, sessionId, name] of [
    ["e2e.json", SESSION_ID, "e2e"],
    ["e2e-tab.json", TAB_SESSION_ID, "e2e-tab"],
    ["e2e-fleet.json", FLEET_SESSION_ID, "Fleet Manager"],
  ] as const) {
    fs.writeFileSync(
      path.join(dir, file),
      JSON.stringify({ sessionId, name, nameSource: "user" }),
    );
  }
}

async function seedLedger(baseURL: string, sessionId: string, mode: string): Promise<void> {
  const res = await fetch(`${baseURL}/api/1.0/session-ledger`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      session_id: sessionId,
      mode,
      location: "local",
      spawned_at: new Date().toISOString(),
    }),
  });
  if (!res.ok) {
    throw new Error(`seed session ledger returned ${res.status}: ${await res.text()}`);
  }
}

export default async function globalSetup(): Promise<void> {
  const frontend = frontendDir();
  const repoRoot = path.resolve(frontend, "..");

  // The binary embeds frontend/dist. Build it once when it is missing rather
  // than serving an empty #root.
  if (!fs.existsSync(path.join(frontend, "dist", "index.html"))) {
    const built = spawnSync("npm", ["run", "build"], { cwd: frontend, stdio: "inherit" });
    if (built.status !== 0) {
      throw new Error("frontend build failed; run `npm --prefix frontend run build`");
    }
  }

  const root = fs.mkdtempSync(path.join(os.tmpdir(), "attention-react-e2e-"));
  const bin = path.join(root, "attention-controller");
  const build = spawnSync("go", ["build", "-o", bin, "."], {
    cwd: repoRoot,
    stdio: "inherit",
  });
  if (build.status !== 0) {
    throw new Error("go build failed for the e2e binary");
  }

  const sessionsDir = path.join(root, "sessions");
  writeSessionRegistry(sessionsDir);

  const ttsURL = await startTTSStub();
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const logPath = path.join(root, "server.log");
  const log = fs.openSync(logPath, "w");

  const proc = spawn(
    bin,
    [
      "-listen", `127.0.0.1:${port}`,
      "-datadir", path.join(root, "data"),
      "-sessions-dir", sessionsDir,
      "-attention-state-dir", path.join(root, "state"),
      "-unforwarded-state-dir", path.join(root, "unforwarded"),
      "-attention-context-dir", path.join(root, "context"),
      "-session-heartbeat-dir", path.join(root, "heartbeat"),
      "-vault-dir", "",
      // Empty disables the legacy pane-addressed jump listener (default is a
      // real address this suite must not bind) and the bearer-token listener.
      "-jump-listen", "",
      "-attention-store-listen", "",
      // Empty leaves the routine-gate settle arm unregistered: it would answer
      // gates in a wrapper process this run does not own.
      "-attention-wrapper-url", "",
      // The stub tts server, so the board renders its read-aloud control as
      // it does in production; the speak specs still intercept the request
      // in the browser to assert its shape.
      "-tts-url", ttsURL,
      "-v", "0",
    ],
    { cwd: repoRoot, detached: true, stdio: ["ignore", log, log] },
  );
  proc.unref();
  fs.closeSync(log);

  if (!proc.pid) throw new Error("binary did not start");
  fs.writeFileSync(
    STATE_PATH,
    JSON.stringify({ baseURL, root, pid: proc.pid, bin, logPath }, null, 2),
  );

  try {
    await waitHealthy(baseURL);
    await waitReady(baseURL);
    await seedLedger(baseURL, SESSION_ID, "headless");
    await seedLedger(baseURL, TAB_SESSION_ID, "interactive");
  } catch (err) {
    try {
      process.kill(-proc.pid, "SIGKILL");
    } catch {
      /* already gone */
    }
    throw err;
  }
}
