// parity.md § View filters — client-only. Each toggle must change the DOM and
// the URL (via replaceState, no navigation) and issue NO item-scoped request.
//
// The "N hidden" count is a known gap: the React board controls carry no
// [data-kind-hidden] element, so that row is a failing test.

import {
  FLEET_SESSION_ID,
  answerViaAPI,
  boardEntry,
  captureItemRequests,
  expect,
  flagViaAPI,
  openBoard,
  pushMessage,
  requestsFor,
  row,
  test,
} from "./support";

test("hide-answered toggles the parked dimmed records and the URL, with no request", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: answered record" });
  await answerViaAPI(request, st, itemID);

  await openBoard(page, st, "?producer_kind=all");
  // Default view parks the answered record.
  await expect(row(page, itemID)).toHaveCount(0);

  const toggle = page.locator("button[data-board-filter]");
  await expect(toggle).toHaveAttribute("aria-checked", "true");
  const seen = captureItemRequests(page);
  const urlBefore = page.url();

  await toggle.click();

  // The record unparks, dimmed, and the URL records the choice.
  await expect(row(page, itemID)).toBeVisible();
  await expect(row(page, itemID)).toHaveClass(/dimmed/);
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  expect(new URL(page.url()).searchParams.get("hide")).toBe("none");
  // replaceState, not a navigation.
  expect(new URL(page.url()).pathname).toBe(new URL(urlBefore).pathname);
  expect(requestsFor(seen, itemID)).toHaveLength(0);

  // Toggling back re-parks it and clears the param.
  await toggle.click();
  await expect(row(page, itemID)).toHaveCount(0);
  expect(new URL(page.url()).searchParams.get("hide")).toBeNull();
});

test("the flag filter is independent of the hide switch", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: flagged record" });
  await flagViaAPI(request, st, itemID);

  await openBoard(page, st, "?producer_kind=all");
  await expect(row(page, itemID)).toHaveCount(0);

  const hideToggle = page.locator("button[data-board-filter]");
  const flagToggle = page.locator("button[data-flag-filter]");
  await expect(flagToggle).toHaveAttribute("aria-checked", "true");
  const seen = captureItemRequests(page);

  await flagToggle.click();
  await expect(row(page, itemID)).toBeVisible();
  await expect(flagToggle).toHaveAttribute("aria-checked", "false");
  expect(new URL(page.url()).searchParams.get("flags")).toBe("none");
  // The hide switch is untouched by the flag switch.
  await expect(hideToggle).toHaveAttribute("aria-checked", "true");
  expect(requestsFor(seen, itemID)).toHaveLength(0);

  await flagToggle.click();
  await expect(row(page, itemID)).toHaveCount(0);
  // The on-spelling is written explicitly, as the retired server-rendered page did.
  expect(new URL(page.url()).searchParams.get("flags")).toBe("flagged");
});

test("the producer-kind link switches the view and the withheld cards appear", async ({
  page,
  request,
  st,
}) => {
  const sessionItem = await pushMessage(request, st, {
    payload: "e2e: session kind",
    producer_kind: "session",
  });
  const managerItem = await pushMessage(request, st, {
    payload: "e2e: manager kind",
    producer_kind: "manager",
  });
  // The session card is a COVERED one: the harness's live Fleet Manager relays
  // it, so the managers-only view withholds it. An uncovered session card is
  // shown on that view instead — see the spec below.
  expect((await boardEntry(request, st, sessionItem)).board.uncovered).toBe(false);

  // No producer_kind param: the board defaults to manager-kind only.
  await openBoard(page, st, "");
  await expect(row(page, managerItem)).toBeVisible();
  await expect(row(page, sessionItem)).toHaveCount(0);

  const link = page.locator("[data-board-kind]");
  await expect(link).toHaveText("Show all kinds");
  await expect(link).toHaveAttribute("data-all-kinds", "false");

  await link.click();

  await expect(row(page, sessionItem)).toBeVisible();
  await expect(row(page, managerItem)).toBeVisible();
  expect(new URL(page.url()).searchParams.get("producer_kind")).toBe("all");
  await expect(page.locator("[data-board-kind]")).toHaveText("Show manager cards");
  await expect(page.locator("[data-board-kind]")).toHaveAttribute("data-all-kinds", "true");
});

test("an uncovered session card is shown on the managers-only view", async ({
  page,
  request,
  st,
}) => {
  // The kind filter's coverage rule: a card no live manager can
  // relay is shown whatever its kind. The Fleet Manager's own card has no
  // relaying parent, so it is uncovered.
  const uncoveredItem = await pushMessage(request, st, {
    payload: "e2e: uncovered session card",
    producer_kind: "session",
    liveness_ref: `session:${FLEET_SESSION_ID}`,
  });
  const coveredItem = await pushMessage(request, st, {
    payload: "e2e: covered session card",
    producer_kind: "session",
  });
  expect((await boardEntry(request, st, uncoveredItem)).board.uncovered).toBe(true);
  expect((await boardEntry(request, st, coveredItem)).board.uncovered).toBe(false);

  await openBoard(page, st, "");
  await expect(row(page, uncoveredItem)).toBeVisible();
  await expect(row(page, uncoveredItem)).toHaveAttribute("data-uncovered", "true");
  await expect(row(page, coveredItem)).toHaveCount(0);
});

test("GAP: the kind view reports how many cards it is withholding", async ({
  page,
  request,
  st,
}) => {
  await pushMessage(request, st, { payload: "e2e: withheld", producer_kind: "session" });
  await openBoard(page, st, "");

  // parity.md: "Producer-kind link: ... renders the 'N hidden' count when cards
  // are withheld." The React controls have no such element.
  await expect(page.locator("[data-kind-hidden]")).toBeVisible();
});

test("the empty state on the all-kinds view reads 'Nothing needs attention.'", async ({
  page,
  st,
}) => {
  // An empty board is established at the read, not by emptying a store other
  // specs share: the page's only input is this one response.
  await page.route("**/api/1.0/attention/board", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "[]" }),
  );

  await openBoard(page, st, "?producer_kind=all");
  await expect(page.locator("p.empty")).toHaveText("Nothing needs attention.");
  await expect(page.locator("li.item")).toHaveCount(0);
});

test("the empty state on a filtered view uses the kind-aware copy", async ({ page, st }) => {
  await page.route("**/api/1.0/attention/board", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", body: "[]" }),
  );

  await openBoard(page, st, "?producer_kind=manager");
  await expect(page.locator("p.empty")).toHaveText(
    "No cards of this kind need attention. The others are hidden by the kind filter above.",
  );
});
