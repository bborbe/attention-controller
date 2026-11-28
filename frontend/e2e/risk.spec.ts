// Risk styling: the card's band, data attributes and blast radius come from the
// SERVER-DERIVED risk (board.risk / board.blast_radius, pkg.RiskOf and
// pkg.BlastRadiusOf), not from the producer's declaration.
//
// Both cases are driven hermetically through the public push API: the risk
// derivation reads the card's own text, so a payload naming an irreversible
// infrastructure operation is enough — no vault task file is needed.
//
// ⚠️ The infra card is pushed as an `ack`, not the default `pick`: the mint-path
// decision-record rule rewrites a pick/message card naming this class into an
// optionless ack announcing it, and the rewritten text no longer carries the
// operation. An ack card is not rewritten, so the render-time rule is what this
// spec observes.

import { expect, openBoard, pushItem, pushMessage, row, test } from "./support";

test("a card naming an irreversible infra operation renders at risk with a blast radius", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushItem(request, st, {
    interrupt_class: "ack",
    answer_mechanism: "ack",
    payload: "move `vg0/data` from `hell` to `nuke-dev`",
  });

  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await expect(card).toHaveAttribute("data-risk", "risk");
  await expect(card).toHaveClass(/risk-risk/);
  await expect(card).toHaveAttribute("data-blast-radius", /Blast radius:/);
  await expect(card.locator(".blast-radius[data-blast-radius-text]")).toContainText(
    "Blast radius:",
  );
});

test("a routine card renders at decision with no blast radius", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, {
    payload: "e2e: approve cleaning the stale build cache?",
  });

  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await expect(card).toHaveAttribute("data-risk", "decision");
  await expect(card).toHaveClass(/risk-decision/);
  await expect(card).not.toHaveAttribute("data-blast-radius", /.*/);
  await expect(card.locator("[data-blast-radius-text]")).toHaveCount(0);
});
