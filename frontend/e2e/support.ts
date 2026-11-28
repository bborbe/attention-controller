// Shared helpers for the React board's Playwright specs.
//
// The suite drives the REAL binary (see global-setup.ts): the same build the
// Go e2e suite starts, serving the embedded frontend/dist at `/`. Every spec
// seeds its own items through the public push API and addresses them by the
// item id the store assigned, so the specs never depend on one another's rows.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test as base } from "@playwright/test";
import type { APIRequestContext, Locator, Page } from "@playwright/test";

export const STATE_PATH = path.join(
  os.tmpdir(),
  "attention-controller-react-e2e-state.json",
);

// The session the fixture registry entry carries. The pushed items name it in
// their liveness_ref, so the store resolves them live and renders them.
export const SESSION_ID = "11111111-1111-4111-8111-111111111111";
export const TAB_SESSION_ID = "22222222-2222-4222-8222-222222222222";
// The live "Fleet Manager" session. Its presence covers the two fixture
// sessions above; a card it raises itself is the one uncovered session card.
export const FLEET_SESSION_ID = "33333333-3333-4333-8333-333333333333";

export interface State {
  baseURL: string;
  root: string;
  pid: number;
  bin: string;
}

export function readState(): State {
  return JSON.parse(fs.readFileSync(STATE_PATH, "utf8")) as State;
}

export const test = base.extend<{ st: State }>({
  // eslint-disable-next-line no-empty-pattern
  st: async ({}, use) => {
    await use(readState());
  },
});

export { expect };

// --- seeding ---------------------------------------------------------------

let seq = 0;
function dedupKey(): string {
  seq += 1;
  return `e2e-${process.pid}-${Date.now()}-${seq}`;
}

export interface PushOverrides {
  producer_kind?: string;
  liveness_ref?: string;
  payload?: string;
  interrupt_class?: string;
  answer_mechanism?: string;
  options?: { label: string; recommended?: boolean; exclusive?: boolean; description?: string }[];
  answer_cardinality?: string;
  questions?: unknown[];
  risk?: string;
}

export function messageCard(overrides: PushOverrides = {}): PushOverrides {
  return {
    answer_mechanism: "message",
    options: [{ label: "alpha" }, { label: "beta" }],
    ...overrides,
  };
}

// pushItem seeds one item through the public push API and returns the item id
// the store assigned.
export async function pushItem(
  request: APIRequestContext,
  st: State,
  overrides: PushOverrides = {},
): Promise<string> {
  const body = {
    producer_id: "e2e",
    producer_kind: "session",
    liveness_ref: `session:${SESSION_ID}`,
    dedup_key: dedupKey(),
    interrupt_class: "pick",
    payload: "e2e: a fixture card",
    ...overrides,
  };
  const res = await request.post(`${st.baseURL}/api/1.0/attention`, { data: body });
  const text = await res.text();
  expect(res.status(), `push failed: ${text}`).toBeLessThan(300);
  const decoded = JSON.parse(text) as { item_id?: string };
  expect(decoded.item_id, `push returned no item_id: ${text}`).toBeTruthy();
  return decoded.item_id as string;
}

export function pushMessage(
  request: APIRequestContext,
  st: State,
  overrides: PushOverrides = {},
): Promise<string> {
  return pushItem(request, st, messageCard(overrides));
}

export function pushPermission(
  request: APIRequestContext,
  st: State,
  payload = "e2e: a permission card",
): Promise<string> {
  return pushItem(request, st, { answer_mechanism: "permission", payload });
}

export function pushAck(
  request: APIRequestContext,
  st: State,
  payload = "e2e: an ack card",
): Promise<string> {
  return pushItem(request, st, { answer_mechanism: "ack", payload });
}

// answerViaAPI answers an item out of band, the way an arm would, so a spec can
// establish an answered/flagged record without clicking the board.
export async function answerViaAPI(
  request: APIRequestContext,
  st: State,
  itemID: string,
): Promise<void> {
  const res = await request.post(`${st.baseURL}/api/1.0/attention/${itemID}/answer`, {
    data: { answered_by: "e2e", answer: { kind: "text", value: "e2e answer" } },
  });
  expect(res.status(), `answer failed: ${await res.text()}`).toBeLessThan(300);
}

export async function flagViaAPI(
  request: APIRequestContext,
  st: State,
  itemID: string,
): Promise<void> {
  const res = await request.post(`${st.baseURL}/api/1.0/attention/${itemID}/flag`, {
    data: { reason: "e2e: misrouted" },
  });
  expect(res.status(), `flag failed: ${await res.text()}`).toBeLessThan(300);
}

export interface StoredItem {
  item_id: string;
  state: string;
  answer?: { kind?: string; value?: string };
  decision?: string;
  rating?: string;
  flag_reason?: string;
  answered_at?: string;
}

export async function getItem(
  request: APIRequestContext,
  st: State,
  itemID: string,
): Promise<StoredItem> {
  const res = await request.get(`${st.baseURL}/api/1.0/attention/${itemID}`);
  expect(res.status()).toBe(200);
  return (await res.json()) as StoredItem;
}

// boardEntry reads one item's entry off the board JSON, including the `board`
// render data the server derives for it (jump target, uncovered marker, …).
export async function boardEntry(
  request: APIRequestContext,
  st: State,
  itemID: string,
): Promise<{ item_id: string; board: { uncovered: boolean; jump_url: string; no_jump: string } }> {
  const res = await request.get(`${st.baseURL}/api/1.0/attention/board`);
  expect(res.status()).toBe(200);
  const entries = (await res.json()) as {
    item_id: string;
    board: { uncovered: boolean; jump_url: string; no_jump: string };
  }[];
  const entry = entries.find((e) => e.item_id === itemID);
  expect(entry, `item ${itemID} is not on the board read`).toBeTruthy();
  return entry!;
}

// --- the board page --------------------------------------------------------

// openBoard loads the board and waits until the React page has rendered its
// controls — App renders them only once the board read resolved, so this is
// the page's own ready signal.
export async function openBoard(page: Page, st: State, query = ""): Promise<void> {
  await page.goto(`${st.baseURL}/${query}`);
  await expect(page.locator("#root .board-controls")).toBeVisible();
}

export function row(page: Page, itemID: string): Locator {
  return page.locator(`li.item[data-item-id="${itemID}"]`);
}

// --- request capture -------------------------------------------------------

export interface Captured {
  method: string;
  url: string;
  path: string;
  body: unknown;
  at: number;
}

function parseBody(raw: string | null): unknown {
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

// captureItemRequests records every request to an item-scoped attention
// endpoint. The board read (/attention/board) and the stream (/attention/stream)
// carry no trailing verb segment and are deliberately not captured.
export function captureItemRequests(page: Page): Captured[] {
  const seen: Captured[] = [];
  page.on("request", (req) => {
    const pathname = new URL(req.url()).pathname;
    if (!/^\/api\/1\.0\/attention\/[^/]+\/[^/]+$/.test(pathname)) return;
    if (pathname.endsWith("/board") || pathname.endsWith("/stream")) return;
    seen.push({
      method: req.method(),
      url: req.url(),
      path: pathname,
      body: parseBody(req.postData()),
      at: Date.now(),
    });
  });
  return seen;
}

export function requestsFor(seen: Captured[], itemID: string, verb?: string): Captured[] {
  return seen.filter((c) => {
    if (c.path.startsWith(`/api/1.0/attention/${itemID}/`)) {
      return verb ? c.path === `/api/1.0/attention/${itemID}/${verb}` : true;
    }
    return false;
  });
}

export function anyItemRequest(seen: Captured[]): Captured[] {
  return seen;
}

export async function waitForRequestTo(
  page: Page,
  itemID: string,
  verb: string,
  timeout = 15000,
): Promise<{ body: unknown }> {
  const req = await page.waitForRequest(
    (r) =>
      r.method() === "POST" &&
      new URL(r.url()).pathname === `/api/1.0/attention/${itemID}/${verb}`,
    { timeout },
  );
  return { body: parseBody(req.postData()) };
}

// bodyAnswer reads the `answer` member of an answer request body.
export function bodyAnswer(body: unknown): Record<string, unknown> {
  const b = (body ?? {}) as Record<string, unknown>;
  return (b.answer ?? {}) as Record<string, unknown>;
}
