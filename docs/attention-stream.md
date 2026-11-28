# The attention change stream

`GET /api/1.0/attention/stream` is the board's change feed: a `text/event-stream` response that
pushes a row when the store changes, so a consumer tracks the board without re-reading it. This
page is the contract — the route, the event, the baseline and the gap every consumer has to close
for itself. The handler is `pkg/handler/attention-stream.go`; where this page and that file
disagree, the file wins and the disagreement is a finding to report.

## What the route is

`GET /api/1.0/attention/stream`, on the board router, answered as `text/event-stream`. It is
**server-to-client only**: nothing is ever read from the connection after the request line, and
the handler opens no request body. A consumer opens it and reads.

## The event shape

Each event is one `data: <json>` line followed by a blank line, and the JSON carries two fields:

| Field | Present | Meaning |
|---|---|---|
| `type` | always | `upsert` for a row that appeared or changed, `remove` for a row that left the board. |
| `item_id` | always | The row this event is about — the key a consumer matches on. |

An earlier version also carried `html`, the server-rendered row. It was dropped with the
server-rendered board: the React board (`frontend/`) ignores frame bodies and re-reads
`GET /api/1.0/attention/board`, and no other consumer read the field.

## It is a change signal, not the item

The event names a row; it is not the record. A consumer that needs the item — its state, its
decision, its answer — reads it back by id, `GET /api/1.0/attention/{item_id}`, when the event
arrives. The React board re-reads the whole board JSON on every frame.

## The baseline

The server takes a **baseline** snapshot of the shared board *before* it writes the response
headers, and flushes the headers immediately after, so a client that has received the headers is
baselined: it is attached to the change feed and its snapshot is the board as of that moment. A
consumer's own read taken before it subscribes is therefore *outside* the stream's baseline.

A change landing between the client's own read and the subscription is **still missed**. The diff
emits only rows whose board entry (the item plus its `board` render data, as
`GET /api/1.0/attention/board` serves it) changed *since the baseline*, so anything already in the
baseline produces no event: an answer that landed before the snapshot is in the baseline, and the
consumer never hears of it. A change landing after the baseline changes its row's entry and emits
an `upsert`; a change landing in the gap is corrected only by the next one.

## What a consumer must therefore do

Subscribe, then read the item once by id immediately after the subscription is established, and
decide from that read. A consumer that only waits for an event silently misses an answer that
landed in the gap — the answer is in the store, and no event ever names it. After that catch-up
read, one item read per matching event is enough.

## What the stream does not promise

- **No keep-alive frames on an idle channel.** Nothing is written while nothing changes, so
  silence is not a dead stream and is never a decision.
- **No reconnection protocol, no replay and no event ids.** A dropped stream is the consumer's to
  handle, and a re-subscribe loop is a poll in disguise: the same re-read the stream exists to
  remove, wrapped in a connection.

## The consumers

`020-answered-signal-folded-into-delivery-arm` and `021-manager-wake-in-process-job` are the two
sibling consumers this contract exists for. Each cites this page rather than restating the event
shape, the baseline rule or the gap.
