// api.ts — the only module that talks to the backend.
import type { BoardItem, Item } from "./types";

export const ATTENTION_URL = "/api/1.0/attention";
export const BOARD_URL = "/api/1.0/attention/board";
export const ATTENTION_STREAM_URL = "/api/1.0/attention/stream";

// fetchBoard reads the board's population: open items plus the answered records
// it renders dimmed, each carrying the render data the server derives for it
// under `board` (jump target, navigation line, uncovered marker, timeline).
// Distinct from fetchItems (GET /attention), which returns only the open items
// an arm should act on.
export async function fetchBoard(signal?: AbortSignal): Promise<BoardItem[]> {
  const response = await fetch(BOARD_URL, { signal });
  if (!response.ok) {
    throw new Error(`GET ${BOARD_URL} failed: ${response.status}`);
  }
  return (await response.json()) as BoardItem[];
}

// prefetchBoard starts the first board read before React mounts, so the board's
// rows are not held behind the first render and effect pass. The first load()
// consumes it through takePrefetchedBoard; every later read fetches afresh.
let prefetched: Promise<BoardItem[]> | null = null;

export function prefetchBoard(): void {
  prefetched = fetchBoard();
  // Handled by the consumer; this only keeps an early failure from surfacing as
  // an unhandled rejection before load() attaches its handler.
  prefetched.catch(() => undefined);
}

export function takePrefetchedBoard(): Promise<BoardItem[]> | null {
  const pending = prefetched;
  prefetched = null;
  return pending;
}

export async function fetchItems(signal?: AbortSignal): Promise<Item[]> {
  const response = await fetch(ATTENTION_URL, { signal });
  if (!response.ok) {
    throw new Error(`GET ${ATTENTION_URL} failed: ${response.status}`);
  }
  return (await response.json()) as Item[];
}

function itemURL(itemID: string, verb: string): string {
  return `${ATTENTION_URL}/${encodeURIComponent(itemID)}/${verb}`;
}

async function postJSON(url: string, body: unknown): Promise<Response> {
  const response = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  return response;
}

// The board's own answered_by declaration. `automation` carries the page's own
// navigator.webdriver reading — the one answered-client member the body may
// carry; the store reads user_agent and remote_addr from the request itself.
export function boardClient() {
  return { answered_by: "attention-board", automation: navigator.webdriver };
}

export function answer(itemID: string, request: unknown): Promise<Response> {
  return postJSON(itemURL(itemID, "answer"), request);
}

export function flag(itemID: string, reason: string): Promise<Response> {
  return postJSON(itemURL(itemID, "flag"), { reason });
}

export function unflag(itemID: string): Promise<Response> {
  return postJSON(itemURL(itemID, "unflag"), {});
}

export function rate(itemID: string, rating: string): Promise<Response> {
  return postJSON(itemURL(itemID, "rate"), { rating });
}

// seen records that the board painted a card. Fire-and-forget: the store stamps
// displayed_at once and ignores later calls; a failure is never surfaced.
export function seen(itemID: string): void {
  void fetch(itemURL(itemID, "seen"), { method: "POST" }).catch(() => undefined);
}

export function speak(itemID: string): Promise<Response> {
  return postJSON(itemURL(itemID, "speak"), {});
}

export function cancel(itemID: string, messageID: string): Promise<Response> {
  return postJSON(itemURL(itemID, "cancel"), { message_id: messageID });
}

export function close(itemID: string): Promise<Response> {
  return postJSON(itemURL(itemID, "close"), boardClient());
}

export function jump(url: string): Promise<Response> {
  return fetch(url, { method: "GET" });
}

// answerKeepalive delivers a held answer as the page unloads. A normal fetch is
// cancelled when the document goes away; `keepalive` lets it outlive the page so
// a held Allow/Deny is never dropped when the operator closes the tab.
export function answerKeepalive(itemID: string, request: unknown): void {
  try {
    void fetch(itemURL(itemID, "answer"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(request),
      keepalive: true,
    });
  } catch {
    // A pagehide delivery is best-effort; a throw here would be lost anyway.
  }
}

// Subscribes to the board's SSE stream. jsdom (Vitest) has no EventSource; the
// guard returns a no-op so a component test never throws.
//
// onTracking reports the stream's health: false on every error, true on the next
// message (a message means the board is tracking the store again). EventSource
// retries a network drop by itself, but a non-200 status or a non-event-stream
// content type closes it for good, so a CLOSED source is reconnected here with
// exponential backoff (1 s floor, 30 s ceiling).
export function subscribeItems(
  onChange: () => void,
  onTracking: (tracking: boolean) => void = () => undefined,
): () => void {
  if (typeof EventSource === "undefined") {
    return () => undefined;
  }
  let source: EventSource | null = null;
  let delayMs = 1000;
  let timer: number | null = null;
  let stopped = false;
  let everOpened = false;

  const connect = () => {
    const current = new EventSource(ATTENTION_STREAM_URL);
    source = current;
    current.onopen = () => {
      delayMs = 1000;
      // A reconnect may have missed changes while the stream was down.
      if (everOpened) onChange();
      everOpened = true;
    };
    current.onerror = () => {
      onTracking(false);
      if (current.readyState !== EventSource.CLOSED) return;
      if (timer !== null || stopped) return;
      timer = window.setTimeout(() => {
        timer = null;
        delayMs = Math.min(delayMs * 2, 30000);
        if (!stopped) connect();
      }, delayMs);
    };
    current.onmessage = () => {
      onTracking(true);
      onChange();
    };
  };
  connect();
  return () => {
    stopped = true;
    if (timer !== null) clearTimeout(timer);
    source?.close();
  };
}
