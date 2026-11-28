// parity.md § Answer form (message cards) — every row is one test here.
// Each asserts the observable post-action delta: the request that left the
// page (method + path + body) AND what the card's subtree did afterwards.

import {
  bodyAnswer,
  captureItemRequests,
  expect,
  openBoard,
  pushMessage,
  requestsFor,
  row,
  test,
  waitForRequestTo,
} from "./support";

test("dismiss posts answer{kind:skip} and the card leaves the board", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: dismiss me" });
  await openBoard(page, st, "?producer_kind=all");
  await expect(row(page, itemID)).toBeVisible();

  const seen = captureItemRequests(page);
  const pending = waitForRequestTo(page, itemID, "answer");
  await row(page, itemID).getByRole("button", { name: "✕ Dismiss" }).click();

  const { body } = await pending;
  expect(bodyAnswer(body)).toEqual({ kind: "skip" });
  expect(seen.filter((c) => c.path === `/api/1.0/attention/${itemID}/answer`)).toHaveLength(1);

  // The answered card leaves the default (hide-answered) board.
  await expect(row(page, itemID)).toHaveCount(0);
});

test("submitting a picked option posts answer{kind:option,value} and the card leaves", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: pick one" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await card.locator('[data-option="beta"]').check();
  await expect(card.locator('[data-option="beta"]')).toBeChecked();

  const pending = waitForRequestTo(page, itemID, "answer");
  await card.getByRole("button", { name: "✓ Submit answer" }).click();

  const { body } = await pending;
  expect(bodyAnswer(body)).toEqual({ kind: "option", value: "beta" });
  await expect(row(page, itemID)).toHaveCount(0);
});

test("typing in Other clears the pick and posts answer{kind:text,value}", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: other text" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await card.locator('[data-option="alpha"]').check();
  await expect(card.locator('[data-option="alpha"]')).toBeChecked();

  await card.locator("input[name=text]").fill("my own words");
  // Typing checks the Other control and clears every pick.
  await expect(card.locator("[data-other]")).toBeChecked();
  await expect(card.locator('[data-option="alpha"]')).not.toBeChecked();

  const pending = waitForRequestTo(page, itemID, "answer");
  await card.getByRole("button", { name: "✓ Submit answer" }).click();

  const { body } = await pending;
  expect(bodyAnswer(body)).toEqual({ kind: "text", value: "my own words" });
});

test("Enter in Other submits the answer, not Dismiss", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: enter submits" });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await card.locator("input[name=text]").fill("sent with enter");

  const pending = waitForRequestTo(page, itemID, "answer");
  await card.locator("input[name=text]").press("Enter");

  const { body } = await pending;
  expect(bodyAnswer(body)).toEqual({ kind: "text", value: "sent with enter" });
  // The load-bearing half: a skip here would mean implicit first-button
  // submission picked Dismiss.
  expect(bodyAnswer(body).kind).not.toBe("skip");
  await expect(row(page, itemID)).toHaveCount(0);
});

test("the corner X hides a message card: answer{kind:hidden}, nothing delivered", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: hide me" });
  await openBoard(page, st, "?producer_kind=all");

  const pending = waitForRequestTo(page, itemID, "answer");
  await row(page, itemID).locator("[data-corner-x]").click();

  const { body } = await pending;
  expect(bodyAnswer(body)).toEqual({ kind: "hidden" });
  await expect(row(page, itemID)).toHaveCount(0);

  // Hidden is distinct from skip: the store records a hidden answer, not a
  // decline.
  const res = await request.get(`${st.baseURL}/api/1.0/attention/${itemID}`);
  const stored = (await res.json()) as { answer?: { kind?: string }; state?: string };
  expect(stored.state).toBe("answered");
  expect(stored.answer?.kind).toBe("hidden");
});

test("multi-question tabs show one panel at a time and keep the other's input", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, {
    payload: "e2e: two questions",
    options: undefined,
    questions: [
      {
        tab: "Alpha",
        payload: "e2e: the first question",
        cardinality: "single",
        options: [{ label: "a1" }, { label: "a2" }],
      },
      {
        tab: "Beta",
        payload: "e2e: the second question",
        cardinality: "single",
        options: [{ label: "b1" }, { label: "b2" }],
      },
    ],
  });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await expect(card.locator('[data-question="Alpha"]')).toBeVisible();
  await expect(card.locator('[data-question="Beta"]')).toHaveCount(0);

  await card.locator('[data-question="Alpha"] input[name=text]').fill("kept across tabs");

  await card.locator('[data-tab="Beta"]').click();
  await expect(card.locator('[data-question="Beta"]')).toBeVisible();
  await expect(card.locator('[data-question="Alpha"]')).toHaveCount(0);
  await expect(card.locator('[data-tab="Beta"]')).toHaveClass(/active/);

  await card.locator('[data-tab="Alpha"]').click();
  await expect(card.locator('[data-question="Alpha"] input[name=text]')).toHaveValue(
    "kept across tabs",
  );
});

test("multi-pick: several picks post answer{values}, the exclusive escape clears them", async ({
  page,
  request,
  st,
}) => {
  const itemID = await pushMessage(request, st, {
    payload: "e2e: multi pick",
    answer_cardinality: "multiple",
    options: [
      { label: "one" },
      { label: "two" },
      { label: "none", exclusive: true },
    ],
  });
  await openBoard(page, st, "?producer_kind=all");

  const card = row(page, itemID);
  await card.locator('[data-option="one"]').check();
  await card.locator('[data-option="two"]').check();
  await expect(card.locator('[data-option="one"]')).toBeChecked();
  await expect(card.locator('[data-option="two"]')).toBeChecked();

  // The exclusive escape clears every other box.
  await card.locator('[data-option="none"]').check();
  await expect(card.locator('[data-option="one"]')).not.toBeChecked();
  await expect(card.locator('[data-option="two"]')).not.toBeChecked();
  await expect(card.locator('[data-option="none"]')).toBeChecked();

  // Any other box clears only the escape.
  await card.locator('[data-option="one"]').check();
  await expect(card.locator('[data-option="none"]')).not.toBeChecked();
  await expect(card.locator('[data-option="one"]')).toBeChecked();

  const pending = waitForRequestTo(page, itemID, "answer");
  await card.getByRole("button", { name: "✓ Submit answer" }).click();

  const { body } = await pending;
  expect(bodyAnswer(body)).toEqual({ kind: "option", values: ["one"] });
  await expect(row(page, itemID)).toHaveCount(0);
});

test("an empty submit shows the note and posts nothing", async ({ page, request, st }) => {
  const itemID = await pushMessage(request, st, { payload: "e2e: empty submit" });
  await openBoard(page, st, "?producer_kind=all");

  const seen = captureItemRequests(page);
  const card = row(page, itemID);
  await card.getByRole("button", { name: "✓ Submit answer" }).click();

  await expect(card.locator(".note.failed")).toHaveText(
    "Pick an option or write an answer first.",
  );
  expect(requestsFor(seen, itemID, "answer")).toHaveLength(0);
  // The card stays on the board: nothing was sent.
  await expect(row(page, itemID)).toBeVisible();
});
