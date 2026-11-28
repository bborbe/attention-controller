// boardLoader.ts — coalesces board reads.
//
// The board re-reads GET /api/1.0/attention/board on every stream frame, and one
// push emits one frame per changed row, so a burst would otherwise fire one full
// board read per frame. The loader keeps at most one read in flight; requests
// that arrive while it runs collapse into a single trailing read, issued once the
// current one settles, so the last state the store reached is always read.
//
// Every read carries a sequence number, and a response older than the last one
// applied is dropped, so an older board can never overwrite a newer one.
//
// There is deliberately no debounce: a click's own refresh and its stream echo
// arrive milliseconds apart, and the e2e rating specs count both re-reads.
export interface BoardLoader {
  request: () => void;
}

export interface BoardLoaderOptions<T> {
  read: () => Promise<T>;
  apply: (board: T) => void;
  fail: (error: Error) => void;
}

export function createBoardLoader<T>({ read, apply, fail }: BoardLoaderOptions<T>): BoardLoader {
  let inFlight = false;
  let trailing = false;
  let issued = 0;
  let applied = 0;

  const run = () => {
    inFlight = true;
    const seq = ++issued;
    let pending: Promise<T>;
    try {
      pending = read();
    } catch (err) {
      pending = Promise.reject(err);
    }
    pending
      .then(
        (board) => {
          if (seq <= applied) return;
          applied = seq;
          apply(board);
        },
        (err: unknown) => {
          if (seq <= applied) return;
          fail(err instanceof Error ? err : new Error(String(err)));
        },
      )
      .finally(() => {
        inFlight = false;
        if (trailing) {
          trailing = false;
          run();
        }
      });
  };

  return {
    request: () => {
      if (inFlight) {
        trailing = true;
        return;
      }
      run();
    },
  };
}
