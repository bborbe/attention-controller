// parity.md § Card states — a held card stays dimmed and visible under the
// answerable filter, the info panel carries the item's timeline, and a card no
// live manager can relay carries the uncovered marker.

import {
  FLEET_SESSION_ID,
  expect,
  openBoard,
  pushMessage,
  pushPermission,
  row,
  test,
} from "./support";

test("a held card renders dimmed with data-held and stays visible", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushPermission(request, st, "e2e: held card");
  await openBoard(page, st, "?producer_kind=all");

  await row(page, itemID).locator('[data-decision="allow"]').click();
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);

  // parity.md: "Held card: renders like a dimmed record but stays visible under
  // the answerable filter (data-held)."
  await expect(row(page, itemID)).toHaveAttribute("data-held", "true");
  await expect(row(page, itemID)).toHaveClass(/dimmed/);
});

test("the info panel shows the item's timeline entries", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: timeline card" });
  await openBoard(page, st, "?producer_kind=all");

  await row(page, itemID).locator("[data-info-toggle]").click();

  // parity.md: "Timeline panel: the card's info panel shows the item's timeline
  // entries."
  await expect(row(page, itemID).locator("[data-timeline]")).toBeVisible();
});

test("a card no live manager can relay carries the uncovered marker", async ({
  page,
  request,
  st,
}) => {
  // Uncovered is resolved only for session-kind cards (pkg/provenance.go
  // uncoveredItems). The harness registers a live Fleet Manager, which covers
  // the fixture sessions; the Fleet Manager's OWN card has no relaying parent,
  // so it is the uncovered session card (pkg/worker-coverage.go).
  const itemID = await pushMessage(request, st, {
    payload: "e2e: uncovered card",
    producer_kind: "session",
    liveness_ref: `session:${FLEET_SESSION_ID}`,
  });
  await openBoard(page, st, "?producer_kind=all");

  // parity.md: "Producer-kind / uncovered markers: a card no live manager can
  // relay is shown whatever its kind."
  await expect(row(page, itemID)).toHaveAttribute("data-uncovered", "true");
});
