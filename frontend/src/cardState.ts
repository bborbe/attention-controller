// cardState.ts — per-card UI state that must outlive the card's DOM node.
//
// A row is keyed on its item id, so an echo (a rating, a flag, a pane
// re-resolve) re-renders the same node and a field being typed in keeps focus.
// The in-progress answer, a typed flag reason, a shown failure note, the info
// panel and the speaking toggle still live here, keyed by item id, so they
// survive a card leaving and re-entering the visible set under a filter. A
// re-push that changes the question resets the picks through syncSignature: a
// pick carried onto a reworded question would answer something the operator
// never read.
import { useSyncExternalStore } from "react";

export interface Note {
  text: string;
  error: boolean;
}

export interface CardState {
  signature: string;
  activeTab: string | null;
  selections: Record<string, string[]>;
  otherText: Record<string, string>;
  flagReason: string;
  note: Note | null;
  speaking: boolean;
  infoOpen: boolean;
  // The rating shown before the store echoes it; null when the stored one shows.
  optimisticRating: string | null;
  // The stored rating the optimistic click was made against.
  optimisticBase: string | null;
}

function fresh(signature: string): CardState {
  return {
    signature,
    activeTab: null,
    selections: {},
    otherText: {},
    flagReason: "",
    note: null,
    speaking: false,
    infoOpen: false,
    optimisticRating: null,
    optimisticBase: null,
  };
}

const states = new Map<string, CardState>();
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

// syncSignature resets the answer state of a card whose question changed. It is
// called from the board read, never during render.
export function syncSignature(itemID: string, signature: string): void {
  const current = states.get(itemID);
  if (!current) return;
  if (current.signature === signature) return;
  states.set(itemID, {
    ...current,
    signature,
    // The open tab is a view choice, not an answer: it survives (the card
    // falls back to the first tab when the re-push dropped it).
    selections: {},
    otherText: {},
  });
  emit();
}

// pruneCardStates drops the state of every card not in keep — called after each
// board read with the board's ids (plus a held answer's), so the map does not
// grow with every item the board ever showed.
export function pruneCardStates(keep: Set<string>): void {
  for (const id of states.keys()) {
    if (!keep.has(id)) states.delete(id);
  }
}

export function getCardState(itemID: string, signature: string): CardState {
  let current = states.get(itemID);
  if (!current) {
    current = fresh(signature);
    states.set(itemID, current);
  }
  return current;
}

export function updateCardState(
  itemID: string,
  patch: Partial<CardState> | ((s: CardState) => Partial<CardState>),
): void {
  const current = states.get(itemID) ?? fresh("");
  const delta = typeof patch === "function" ? patch(current) : patch;
  states.set(itemID, { ...current, ...delta });
  emit();
}

export function useCardState(itemID: string, signature: string): CardState {
  return useSyncExternalStore(subscribe, () => getCardState(itemID, signature));
}
