// api.ts — the only module that talks to the backend.
import type { Item } from "./types";

export const ATTENTION_URL = "/api/1.0/attention";
export const ATTENTION_STREAM_URL = "/api/1.0/attention/stream";

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

export function seen(itemID: string): Promise<Response> {
  return postJSON(itemURL(itemID, "seen"), {});
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

// Subscribes to the board's SSE stream. jsdom (Vitest) has no EventSource; the
// guard returns a no-op so a component test never throws.
export function subscribeItems(onChange: () => void): () => void {
  if (typeof EventSource === "undefined") {
    return () => undefined;
  }
  const source = new EventSource(ATTENTION_STREAM_URL);
  source.onmessage = () => onChange();
  return () => source.close();
}
