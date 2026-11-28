// parity.md § Card states — three rows with no React implementation yet. Each
// is a failing test rather than a weakened one: the parity checklist names the
// observable, and the React page does not emit it.

import {
  expect,
  openBoard,
  pushMessage,
  pushPermission,
  row,
  test,
} from "./support";

test("GAP: a held card renders dimmed with data-held and stays visible", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushPermission(request, st, "e2e: held card");
  await openBoard(page, st, "?producer_kind=all");

  await row(page, itemID).locator('[data-decision="allow"]').click();
  await expect(page.locator("[data-undo-bar]")).toHaveClass(/is-active/);

  // parity.md: "Held card: renders like a dimmed record but stays visible under
  // the answerable filter (data-held)." The React board removes the held card
  // from the visible list instead of marking it.
  await expect(row(page, itemID)).toHaveAttribute("data-held", "true");
  await expect(row(page, itemID)).toHaveClass(/dimmed/);
});

test("GAP: the info panel shows the item's timeline entries", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: timeline card" });
  await openBoard(page, st, "?producer_kind=all");

  await row(page, itemID).locator("[data-info-toggle]").click();

  // parity.md: "Timeline panel: the card's info panel shows the item's timeline
  // entries." The React panel shows item/producer/state/risk only.
  await expect(row(page, itemID).locator("[data-timeline]")).toBeVisible();
});

test("GAP: a card no live manager can relay carries the uncovered marker", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, {
    payload: "e2e: uncovered card",
    producer_kind: "manager",
  });
  await openBoard(page, st, "?producer_kind=all");

  // parity.md: "Producer-kind / uncovered markers: a card no live manager can
  // relay is shown whatever its kind." The React card emits no marker.
  await expect(row(page, itemID)).toHaveAttribute("data-uncovered", "true");
});
