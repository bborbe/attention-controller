// SC1's page-identity half: the served document is the React build, not the
// vanilla page. These assertions name elements only the React app emits
// (#root populated, the [data-undo-bar] hold bar), so a stale bundle or a
// fallback to the old page fails here first.

import {
  expect,
  openBoard,
  pushMessage,
  row,
  test,
} from "./support";

test("serves the React page: #root is populated and the undo bar exists", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: react page identity" });

  await openBoard(page, st, "?producer_kind=all");

  // #root is the React mount point. The vanilla page inlined its markup into
  // the body and had no #root, so a populated #root is React-only.
  await expect(page.locator("#root")).not.toBeEmpty();
  await expect(page.locator("#root .board-controls")).toBeVisible();

  // The hold bar is a React component; it ships in every render, inactive.
  await expect(page.locator("[data-undo-bar]")).toHaveCount(1);
  await expect(page.locator("[data-undo-bar]")).not.toHaveClass(/is-active/);

  // The card is rendered inside the React list, addressed by the store id.
  await expect(row(page, itemID)).toBeVisible();
  await expect(row(page, itemID).locator("[data-speak]")).toHaveCount(1);
});

test("renders one row per pushed card and keeps each card's own controls", async ({
  page,
  request,
  st,
}) => {
  const first = await pushMessage(request, st, { payload: "e2e: first row" });
  const second = await pushMessage(request, st, { payload: "e2e: second row" });

  await openBoard(page, st, "?producer_kind=all");

  await expect(row(page, first)).toBeVisible();
  await expect(row(page, second)).toBeVisible();
  // Both rows carry the board's own item id and producer kind as attributes —
  // the React card's data contract.
  await expect(row(page, first)).toHaveAttribute("data-producer-kind", "session");
  await expect(row(page, second).locator(".card-title")).toHaveText("e2e: second row");
});
