// parity.md § Card controls — rate, flag, unflag, speak/cancel, info toggle.
// The info toggle is client-only and is asserted to issue NO item-scoped
// request; every other control is asserted by METHOD + PATH + body.
//
// Jump and seen are known gaps and are marked fixme with their reason: the
// board JSON carries no jump_url and the React card renders no jump control,
// and nothing in the page calls the seen endpoint.

import {
  captureItemRequests,
  expect,
  flagViaAPI,
  openBoard,
  pushMessage,
  requestsFor,
  row,
  test,
  waitForRequestTo,
} from "./support";

test("rating 👍 posts /rate{up}, and clicking it again clears the rating", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: rate me" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  const up = card.locator('[data-rate="up"]');
  await expect(up).toHaveAttribute("aria-pressed", "false");

  const first = waitForRequestTo(page, itemID, "rate");
  await up.click();
  expect((await first).body).toEqual({ rating: "up" });
  await expect(up).toHaveAttribute("aria-pressed", "true");
  await expect(up).toHaveClass(/rated/);

  // Clicking the active icon clears it.
  const second = waitForRequestTo(page, itemID, "rate");
  await up.click();
  expect((await second).body).toEqual({ rating: "" });
  await expect(up).toHaveAttribute("aria-pressed", "false");
  await expect(card.locator('[data-rating=""]')).toHaveCount(1);
});

test("switching the rating posts the other value and moves the highlight", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: switch rating" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  const up = waitForRequestTo(page, itemID, "rate");
  await card.locator('[data-rate="up"]').click();
  await up;

  const down = waitForRequestTo(page, itemID, "rate");
  await card.locator('[data-rate="down"]').click();
  expect((await down).body).toEqual({ rating: "down" });
  await expect(card.locator('[data-rate="down"]')).toHaveAttribute("aria-pressed", "true");
  await expect(card.locator('[data-rate="up"]')).toHaveAttribute("aria-pressed", "false");
});

test("flagging: an empty reason shows the note and posts nothing; a reason flags the card", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: flag me" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  const seen = captureItemRequests(page);
  await card.getByRole("button", { name: "⚑ Flag as misrouted" }).click();
  await expect(card.locator("[data-flag-control]")).toBeVisible();

  await card.getByRole("button", { name: "Submit flag" }).click();
  await expect(card.locator(".note.failed")).toHaveText(
    "Say why this card should not have reached you.",
  );
  expect(requestsFor(seen, itemID, "flag")).toHaveLength(0);
  // A flag must never travel the answer path: that would set answered_at and
  // destroy the distinction the feature exists for.
  expect(requestsFor(seen, itemID, "answer")).toHaveLength(0);

  await card.locator("[data-flag-control] input").fill("e2e: wrong board");
  const pending = waitForRequestTo(page, itemID, "flag");
  await card.getByRole("button", { name: "Submit flag" }).click();
  expect((await pending).body).toEqual({ reason: "e2e: wrong board" });

  // The flagged card parks: the flag filter hides it by default.
  await expect(row(page, itemID)).toHaveCount(0);
});

test("unflag posts /unflag and the card returns from the flagged park", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: unflag me" });
  await flagViaAPI(request, st, itemID);

  await openBoard(page, st, "?producer_kind=all&flags=none");
  const card = row(page, itemID);
  await expect(card.locator("[data-unflag]")).toBeVisible();

  const pending = waitForRequestTo(page, itemID, "unflag");
  await card.locator("[data-unflag]").click();
  expect((await pending).body).toEqual({});

  // The card is no longer flagged: Unflag gives way to the flag affordance.
  await expect(card.locator("[data-unflag]")).toHaveCount(0);
  await expect(card.getByRole("button", { name: "⚑ Flag as misrouted" })).toBeVisible();
  await expect(row(page, itemID)).toBeVisible();
});

test("speak starts reading and cancel stops it", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: speak me" });
  await openBoard(page, st, "?producer_kind=all");

  // The server's read-aloud route is disabled in this harness; the control's
  // contract is the request it posts, so both routes are fulfilled in the
  // browser.
  await page.route("**/api/1.0/attention/*/speak", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "{}" }),
  );
  await page.route("**/api/1.0/attention/*/cancel", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "{}" }),
  );

  const card = row(page, itemID);
  const speak = card.locator("[data-speak]");
  await expect(speak).toHaveAttribute("aria-pressed", "false");
  await expect(speak).toHaveAttribute("aria-label", "Read aloud");

  const speakReq = waitForRequestTo(page, itemID, "speak");
  await speak.click();
  await speakReq;
  await expect(speak).toHaveAttribute("aria-pressed", "true");
  await expect(speak).toHaveAttribute("aria-label", "Stop reading");

  const cancelReq = waitForRequestTo(page, itemID, "cancel");
  await speak.click();
  expect((await cancelReq).body).toEqual({ message_id: "" });
  await expect(speak).toHaveAttribute("aria-pressed", "false");
  await expect(speak).toHaveAttribute("aria-label", "Read aloud");
});

test("the info toggle is client-only: it shows the panel and posts no request", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: info toggle" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  const seen = captureItemRequests(page);
  const urlBefore = page.url();

  await expect(card.locator("[data-info-panel]")).toHaveCount(0);
  await card.locator("[data-info-toggle]").click();
  await expect(card.locator("[data-info-panel]")).toBeVisible();
  await expect(card.locator("[data-info-panel]")).toContainText(itemID);

  await card.locator("[data-info-toggle]").click();
  await expect(card.locator("[data-info-panel]")).toHaveCount(0);

  expect(requestsFor(seen, itemID)).toHaveLength(0);
  expect(page.url()).toBe(urlBefore);
});

test("jump control", async () => {
  // The board JSON carries no jump_url and Card.tsx renders no jump control,
  // so the parity row has no observable in the React page yet.
  test.fixme(true, "no jump control: the board JSON carries no jump_url and Card renders none");
});

test("seen endpoint", async () => {
  // api.ts exports seen(), but no component calls it — opening a card issues
  // no POST /api/1.0/attention/<id>/seen.
  test.fixme(true, "the React board never calls POST /api/1.0/attention/<id>/seen");
});
