// itemView.ts — pure derivations from a board item, mirroring the Go page
// builders (pkg/handler/attention-page-helpers.go, attention-page-timeline.go)
// so the React card renders what the server-rendered board rendered.
import type { Answer, Item } from "./types";

// isAnswered: the item is a record (answered, out of the open set).
export function isAnswered(item: Item): boolean {
  return item.state === "answered";
}

// isStoreHeld: answered but not yet delivered — still OPEN, so it stays under
// the answerable filter, but it renders as a record with no answer controls.
export function isStoreHeld(item: Item): boolean {
  return item.state === "open" && !!item.answered_at;
}

export function isDimmed(item: Item): boolean {
  return isAnswered(item) || isStoreHeld(item);
}

// signatureOf is what a card's picks are bound to: the question as worded.
export function signatureOf(item: Item): string {
  return JSON.stringify([
    item.payload,
    item.options ?? null,
    item.answer_cardinality ?? null,
    item.questions ?? null,
  ]);
}

// renderKeyOf changes whenever the card's rendered content changes, so the row
// is re-mounted (a fresh node) exactly when the store changed it. displayed_at
// is excluded: the board's own seen stamp is not a change the operator caused.
export function renderKeyOf(item: Item): string {
  const { displayed_at: _displayed, ...rest } = item as Item & { displayed_at?: unknown };
  return `${item.item_id}|${JSON.stringify(rest)}`;
}

function answerText(answer: Answer): string {
  switch (answer.kind) {
    case "skip":
      return "skipped";
    case "hidden":
      return "hidden (handled elsewhere)";
    default:
      if (answer.values && answer.values.length > 0) return answer.values.join(", ");
      return answer.value ?? "";
  }
}

export function recordAnswer(item: Item): string {
  if (item.answer_mechanism === "permission") {
    return item.decision ? `decision: ${item.decision}` : "no decision recorded";
  }
  if (item.answers && item.answers.length > 0) {
    // QuestionAnswer embeds Answer, so on the wire its fields sit flat beside
    // `question` (tygo renders the embedding as a nested `Answer` member).
    return item.answers
      .map((a) => `${a.question}: ${answerText(a as unknown as Answer)}`)
      .join(" · ");
  }
  if (item.answer) return answerText(item.answer);
  return "no answer recorded";
}

export function recordClient(item: Item): string {
  const client = item.answered_client;
  if (!client) return "";
  const parts: string[] = [];
  if (client.user_agent) parts.push(client.user_agent);
  if (client.remote_addr) parts.push(`@ ${client.remote_addr}`);
  let rendered = parts.join(" ").trim();
  if (client.forwarded_for) {
    rendered = rendered
      ? `${rendered} (forwarded_for: ${client.forwarded_for})`
      : `forwarded_for: ${client.forwarded_for}`;
  }
  return rendered;
}

const ABSENT = "—";

function stamp(at: unknown): string {
  return typeof at === "string" && at ? at : ABSENT;
}

function delta(from: unknown, to: unknown): string {
  if (typeof from !== "string" || typeof to !== "string" || !from || !to) return ABSENT;
  const a = Date.parse(from);
  const b = Date.parse(to);
  if (Number.isNaN(a) || Number.isNaN(b)) return ABSENT;
  return `${Math.floor((b - a) / 1000)}s`;
}

// timelineOf mirrors attentionTimeline. The board JSON carries no delivery
// report, which is the Go builder's fail-soft case: no delivery stage.
export function timelineOf(item: Item): string[] {
  const lines = [
    `asked ${stamp(item.asked_at)}`,
    `created ${stamp(item.created_at)}`,
    `displayed ${stamp(item.displayed_at)}`,
    `asked→created ${delta(item.asked_at, item.created_at)}`,
    `created→displayed ${delta(item.created_at, item.displayed_at)}`,
    `answered ${stamp(item.answered_at)}`,
    `displayed→answered ${delta(item.displayed_at, item.answered_at)}`,
  ];
  if (item.answered_by) lines.push(`answered by ${item.answered_by}`);
  lines.push(`delivered ${ABSENT}`, `answered→delivered ${ABSENT}`, `released ${ABSENT}`);
  if (item.closed_at) {
    let close = `closed ${stamp(item.closed_at)} ${item.answer_mechanism}`;
    const client = recordClient(item);
    if (client) close += ` via ${client}`;
    lines.push(close);
  } else {
    lines.push(`closed ${ABSENT}`);
  }
  return lines;
}

// answerFailureMessage reads the store's error envelope; a stale card (the item
// moved underneath the page) gets the operator-facing line, anything else the
// raw body.
export function answerFailureMessage(status: number, body: string): { text: string; stale: boolean } {
  try {
    const envelope = JSON.parse(body) as {
      error?: { code?: string; details?: { closed_at?: string } };
    };
    const failure = envelope?.error;
    if (failure?.code === "ITEM_CLOSED") {
      const closedAt = failure.details?.closed_at ?? "";
      return {
        stale: true,
        text: closedAt
          ? `This item was already closed at ${closedAt} - it left the queue before this answer arrived. Returning to the queue.`
          : "This item had already left the queue before this answer arrived. Returning to the queue.",
      };
    }
    if (failure?.code === "ALREADY_ANSWERED") {
      return {
        stale: true,
        text: "This item was already answered. Returning to the queue to show what was recorded.",
      };
    }
  } catch {
    // Not an envelope: fall through to the raw body.
  }
  return { stale: false, text: `Answer failed - HTTP ${status} - ${body}` };
}
