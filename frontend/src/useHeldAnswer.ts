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
export function useHeldAnswer(
  onChanged: () => void,
  onFailed: (itemID: string, status: number, body: string) => void = () => undefined,
): HeldAnswerApi {
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
      void answer(pending.itemID, { ...boardClient(), decision: pending.decision })
        .then(async (response) => {
          if (!response.ok) {
            onFailed(pending.itemID, response.status, await response.text());
          }
          onChanged();
        })
        .catch((error: unknown) => {
          onFailed(pending.itemID, 0, String(error));
          onChanged();
        });
    },
    [clearTimer, onChanged, onFailed],
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
      // Answered elsewhere (a record, or an open item carrying another arm's
      // answer) or gone from the board (closed): the hold is moot.
      const found = items.find((item) => item.item_id === current.itemID);
      if (!found || found.state !== "open" || found.answered_at) {
        clearTimer();
        heldRef.current = null;
        setHeld(null);
      }
    },
    [clearTimer],
  );

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      // Never steal a keystroke: a held modifier is another shortcut, and a
      // focused field is the operator typing.
      if (event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }
      const target = event.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)
      ) {
        return;
      }
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
        // The hold timer must not survive the page: a back/forward-cache restore
        // would fire it and POST the answer a second time.
        clearTimer();
      }
    };
    window.addEventListener("pagehide", onHide);
    return () => window.removeEventListener("pagehide", onHide);
  }, [clearTimer]);

  return { held, decide, undo, cancelIfAnswered };
}
