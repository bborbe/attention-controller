// Unit specs for the board read coalescer: a burst of stream frames must not
// fan out into one board read per frame, and an older board must never replace
// a newer one.
import { createBoardLoader } from "./boardLoader";

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (err: Error) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (err: Error) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

// fakeFetch hands out one pending response per read, so a spec decides when
// (and in which order) each read settles.
function fakeFetch() {
  const calls: Deferred<string>[] = [];
  const read = vi.fn(() => {
    const d = deferred<string>();
    calls.push(d);
    return d.promise;
  });
  return { read, calls };
}

describe("createBoardLoader", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("collapses N rapid change signals into at most two board reads", async () => {
    const { read, calls } = fakeFetch();
    const applied: string[] = [];
    const loader = createBoardLoader({ read, apply: (b) => applied.push(b), fail: () => undefined });

    for (let i = 0; i < 25; i++) loader.request();
    await vi.runAllTimersAsync();
    expect(read).toHaveBeenCalledTimes(1);

    calls[0].resolve("board-1");
    await vi.runAllTimersAsync();
    // The frames that arrived while the first read ran become ONE trailing read.
    expect(read).toHaveBeenCalledTimes(2);

    calls[1].resolve("board-2");
    await vi.runAllTimersAsync();
    expect(read).toHaveBeenCalledTimes(2);
    expect(applied).toEqual(["board-1", "board-2"]);
  });

  it("never lets a stale response replace a newer one", async () => {
    const { read, calls } = fakeFetch();
    const applied: string[] = [];
    const loader = createBoardLoader({ read, apply: (b) => applied.push(b), fail: () => undefined });

    loader.request();
    loader.request(); // arrives mid-read: queued as the trailing read
    calls[0].resolve("old");
    await vi.runAllTimersAsync();
    expect(read).toHaveBeenCalledTimes(2);

    loader.request(); // arrives while the trailing read runs
    calls[1].resolve("new");
    await vi.runAllTimersAsync();
    calls[2].resolve("newest");
    await vi.runAllTimersAsync();

    // Applied strictly in issue order; the last board on screen is the newest.
    expect(applied).toEqual(["old", "new", "newest"]);
    expect(applied[applied.length - 1]).toBe("newest");
  });

  it("keeps the last good board on a failed refresh and still runs the trailing read", async () => {
    const { read, calls } = fakeFetch();
    const applied: string[] = [];
    const failures: string[] = [];
    const loader = createBoardLoader({
      read,
      apply: (b) => applied.push(b),
      fail: (e) => failures.push(e.message),
    });

    loader.request();
    calls[0].resolve("good");
    await vi.runAllTimersAsync();

    loader.request();
    loader.request();
    calls[1].reject(new Error("HTTP 500"));
    await vi.runAllTimersAsync();
    expect(applied).toEqual(["good"]);
    expect(failures).toEqual(["HTTP 500"]);

    // The frame that arrived during the failed read is not lost.
    expect(read).toHaveBeenCalledTimes(3);
    calls[2].resolve("recovered");
    await vi.runAllTimersAsync();
    expect(applied).toEqual(["good", "recovered"]);
  });
});
