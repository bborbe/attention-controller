import { useCallback, useEffect, useRef, useState } from "react";
import { answer, answerKeepalive, boardClient } from "./api";
import type { Item } from "./types";

export interface HeldAnswer {
  itemID: string;
  decision: "allow" | "deny";
}

export interface HeldAnswerApi {
  held: HeldAnswer | null;
  decide: (itemID: string, decision: "allow" | "deny") => void;
  undo: () => void;
  cancelIfAnswered: (items: Item[]) => void;
}

// The hold window: long enough to undo a misclick, short enough that the asking
// session is not kept waiting. The e2e contract measures a 4–5 s
// click-to-delivery delta, so this value is load-bearing.
const HOLD_MS = 4500;
// A decision inside this window after the previous one is swallowed, so the
// second half of a double-click cannot answer the next card.
const SUPPRESS_MS = 400;

// useHeldAnswer holds a permission card's Allow/Deny for HOLD_MS before the
// answer reaches the store. Undo (button or `U`) cancels it; answering another
// card flushes it; leaving the page delivers it with keepalive so a held answer
// is never dropped. The hold is client-side — the schema has no reopen endpoint.
export function useHeldAnswer(onChanged: () => void): HeldAnswerApi {
  const [held, setHeld] = useState<HeldAnswer | null>(null);
  const timerRef = useRef<number | null>(null);
  const heldRef = useRef<HeldAnswer | null>(null);
  const lastDecisionRef = useRef<number>(0);

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const deliver = useCallback(
    (pending: HeldAnswer) => {
      clearTimer();
      heldRef.current = null;
      setHeld(null);
      void answer(pending.itemID, { ...boardClient(), decision: pending.decision }).then(() =>
        onChanged(),
      );
    },
    [clearTimer, onChanged],
  );

  const decide = useCallback(
    (itemID: string, decision: "allow" | "deny") => {
      const now = Date.now();
      if (now - lastDecisionRef.current < SUPPRESS_MS) {
        return;
      }
      lastDecisionRef.current = now;
      const previous = heldRef.current;
      if (previous && previous.itemID !== itemID) {
        deliver(previous);
      }
      const next: HeldAnswer = { itemID, decision };
      heldRef.current = next;
      setHeld(next);
      clearTimer();
      timerRef.current = window.setTimeout(() => deliver(next), HOLD_MS);
    },
    [clearTimer, deliver],
  );

  const undo = useCallback(() => {
    clearTimer();
    heldRef.current = null;
    setHeld(null);
    onChanged();
  }, [clearTimer, onChanged]);

  const cancelIfAnswered = useCallback(
    (items: Item[]) => {
      const current = heldRef.current;
      if (!current) {
        return;
      }
      const found = items.find((item) => item.item_id === current.itemID);
      if (found && found.state === "answered") {
        clearTimer();
        heldRef.current = null;
        setHeld(null);
      }
    },
    [clearTimer],
  );

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.key === "u" || event.key === "U") && heldRef.current) {
        event.preventDefault();
        undo();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [undo]);

  useEffect(() => {
    const onHide = () => {
      const current = heldRef.current;
      if (current) {
        answerKeepalive(current.itemID, { ...boardClient(), decision: current.decision });
        heldRef.current = null;
      }
    };
    window.addEventListener("pagehide", onHide);
    return () => window.removeEventListener("pagehide", onHide);
  }, []);

  return { held, decide, undo, cancelIfAnswered };
}
