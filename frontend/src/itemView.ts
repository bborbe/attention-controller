// itemView.ts — pure derivations from a board item. The server-derived half
// (jump target, navigation links, timeline, delivery outcome) arrives in
// item.board, built by pkg/handler/attention-board-row.go; this file derives the
// rest so the React card renders what the retired server-rendered board did.
import type { Answer, BoardItem, BoardView, Item } from "./types";

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
// The board's timeline is excluded for the same reason: its `displayed` line is
// derived from displayed_at.
export function renderKeyOf(item: BoardItem): string {
  const { displayed_at: _displayed, board, ...rest } = item as BoardItem & { displayed_at?: unknown };
  const { timeline: _timeline, ...view } = boardViewOf({ board } as BoardItem);
  return `${item.item_id}|${JSON.stringify(rest)}|${JSON.stringify(view)}`;
}

// EMPTY_VIEW is the render data of an entry the board read carried none for:
// every derived value absent, so the card renders exactly what it renders for
// an item whose provenance did not resolve.
const EMPTY_VIEW: BoardView = {
  jump_url: "",
  jump_command: "",
  no_jump: "",
  uncovered: false,
  provenance: {
    task_name: "",
    task_href: "",
    goal_name: "",
    goal_href: "",
    topic_name: "",
    topic_href: "",
    session_name: "",
    host: "",
    cwd: "",
    tool: "",
    pane: "",
    pane_unvalidated: false,
    unroutable: false,
  },
  timeline: [],
  delivery_status: "",
  delivery_outcome: "",
  undeliverable: false,
  speak_enabled: false,
  risk: "",
  blast_radius: "",
};

// boardViewOf returns the entry's server-derived render data
// (pkg/handler/attention-board-view.go), or EMPTY_VIEW when the entry carries
// none.
export function boardViewOf(item: BoardItem): BoardView {
  return item.board ?? EMPTY_VIEW;
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
