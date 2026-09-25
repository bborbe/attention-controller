// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"bytes"
	"context"
	"html/template"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

// attentionPageTemplate is the whole page: one document, inline styles, no
// external stylesheet, no framework, no build step. It lives here as a string
// constant so the page ships inside the binary rather than as an asset.
//
// The document renders answer controls for `message` items, and a jump handover
// for every item whose session resolves to a pane. ⚠️ Both reverse the decision
// this page was built with, which was that the document is inert — "no <form>,
// no submitting <button>, no <script> and no fetch/XHR: the page only reads, so
// an answer or close control would be a defect rather than a missing feature".
//
// The reversal is bounded by the schema's who-answers-what ruling rather than
// by taste. A `message` item is `pick`-shaped, so a control that gives a `pick`
// answer is a legal way to answer it. A `permission` item is `approve`-shaped
// and **only the operator may answer it, in the session that raised it** — an
// answer control there would be the permission laundering the schema forbids —
// so a `permission` item renders no answer controls at all. The schema's
// § Answer routing records the board as a second arm
// (`answered_by: attention-board`) and states this reversal; silence 12 is the
// field change that made it possible.
//
// ⚠️ The jump handover is a button as well as a copyable command, and that
// reverses a second decision recorded here. It was a command only, on the
// reasoning that "a browser cannot activate a WezTerm tab, so an anchor here
// would present a value as resolved that is not". That premise is false against
// the fleet-jump server, which activates a pane from a plain HTTP GET — proven
// end-to-end on 2026-09-24 — so a link does resolve. The command stays, because
// an operator may still want to paste it, and the button is added beside it.
//
// The button's target is a path on *this* board, never the fleet-jump URL: that
// URL carries the shared token as a query parameter, and the token must not
// reach the browser. The path is called in the background, and the board
// performs the jump server-side, so the token stays on the server and out of
// the served document — and the click leaves the page where it was, which is
// the behaviour the operator asked for.
//
// Rendering goes through html/template, which escapes every interpolated value
// for the context it lands in. ProducerID is a producer-supplied free string
// (validated only by NotEmptyString) and the provenance values are read from
// the producer's own event log, so that escaping is what keeps a producer from
// injecting markup into the reader's browser.
//
// The provenance line renders one span per resolved value and omits the rest.
// ⚠️ An unresolved value is *omitted*, never filled with a placeholder — a
// stand-in like `unknown` or `n/a` would be an unresolvable value presented as
// resolved, which [[Attention Item Schema]] § Silence 7 forbids. A row with no
// resolvable provenance at all renders no provenance line, which is exactly
// what this page rendered before the change.
const attentionPageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Attention</title>
<style>
/* Dark theme, matching the tts-mcp page this store's UI is modelled on — same
   token names and values, so the two surfaces read as one family. The
   color-scheme property is set so the scrollbar and any native control render
   dark too; without it the page is dark but the chrome around it stays light. */
:root {
  color-scheme: dark;
  --bg: #111418;
  --panel: #1a1f26;
  --border: #2a3038;
  --text: #e8edf2;
  --muted: #8b95a3;
  --warn: #d08b5b;
}
* { box-sizing: border-box; }
body {
  margin: 0 auto;
  max-width: 760px;
  padding: 24px;
  background: var(--bg);
  color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
h1 { font-size: 20px; margin: 0 0 16px; }
ul.items { list-style: none; padding: 0; margin: 0; }
li.item {
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 16px;
  margin-bottom: 12px;
}
.producer {
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  letter-spacing: 0.04em;
}
.payload { font-size: 15px; line-height: 1.45; margin: 6px 0 8px; white-space: pre-wrap; }
.provenance {
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  margin: 0 0 6px;
}
/* One separator between spans, so the line reads as one sentence without the
   template emitting trailing separators for values that were omitted. */
.provenance span + span::before { content: " · "; }
.provenance .unroutable { color: var(--warn); }
.meta { color: var(--muted); font-size: 12px; }
.empty { color: var(--muted); font-size: 14px; }
/* Answer controls, rendered for message items only. The context line carries
   the background the producer declared, kept separate from the question so the
   ask stays readable on its own at the top of the row. */
.context { color: var(--muted); font-size: 13px; line-height: 1.45; margin: 0 0 8px; white-space: pre-wrap; }
.answer { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 10px 0 0; }
.answer button {
  background: var(--panel);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 12px;
  font-size: 13px;
  cursor: pointer;
}
.answer button:hover { border-color: var(--muted); }
.answer button.recommended { border-color: var(--warn); }
.answer .rec { color: var(--warn); font-size: 11px; }
.answer input[type=text] {
  flex: 1 1 200px;
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 10px;
  font-size: 13px;
}
/* The jump handover: a copyable command for the operator who wants to paste it,
   and a button for the one who wants to click. The button's href is a path on
   this board, which redirects to the fleet-jump URL server-side — the token is
   a query parameter of that URL and never reaches the document. */
.jump { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; color: var(--muted); font-size: 12px; margin: 8px 0 0; }
.jump code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 2px 6px;
  user-select: all;
}
.jump-button {
  background: var(--panel);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 6px 12px;
  font-size: 13px;
  font-family: inherit;
  cursor: pointer;
}
.jump-button:hover { border-color: var(--muted); }
/* A control's outcome is shown, never swallowed into a reload — a silent catch
   reports a code fault as a connection problem. The class is "note" rather than
   "failed" because the read-aloud control reports success through it too (the
   tts message id), and a success line rendered in a failure style would read as
   an error. */
.note { color: var(--muted); font-size: 12px; margin: 8px 0 0; }
.note.failed { color: var(--warn); }
</style>
</head>
<body>
<h1>Attention</h1>
{{if .Items}}<ul class="items">
{{range .Items}}<li class="item" data-item-id="{{ .Item.ItemID }}">
<div class="producer">{{ .Item.ProducerID }} ({{ .Item.ProducerKind }})</div>
<div class="payload">{{ .Item.Payload }}</div>
{{if .Item.Context}}<div class="context">{{ .Item.Context }}</div>
{{end}}{{if .Provenance.Resolved}}<div class="provenance">{{if .Provenance.Host}}<span class="host">{{ .Provenance.Host }}</span>{{end}}{{if .Provenance.Cwd}}<span class="cwd">{{ .Provenance.Cwd }}</span>{{end}}{{if .Provenance.Tool}}<span class="tool">{{ .Provenance.Tool }}</span>{{end}}{{if .Provenance.Pane}}<span class="pane">pane {{ .Provenance.Pane }}</span>{{else if .Provenance.PaneRecorded}}<span class="unroutable">unroutable</span>{{end}}</div>
{{end}}{{if .Message}}<form class="answer">
{{range .Item.Options}}<button type="submit" name="kind" value="option" data-value="{{ .Label }}"{{if .Recommended}} class="recommended"{{end}}>{{ .Label }}{{if .Recommended}} <span class="rec">recommended</span>{{end}}</button>
{{end}}<button type="submit" name="kind" value="skip">Skip</button>
<input type="text" name="text" placeholder="or answer in your own words">
<button type="submit" name="kind" value="text">Send</button>
{{if $.Speak}}<button type="button" class="speak" data-speak>Read aloud</button>
{{end}}</form>
{{end}}{{if or .Jump .JumpURL}}<div class="jump">{{if .Jump}}<span>Approve in the session that asked: <code>{{ .Jump }}</code></span>{{end}}{{if .JumpURL}}<button type="button" class="jump-button" data-jump="{{ .JumpURL }}">Jump to session</button>{{end}}</div>
{{end}}<div class="meta">{{ .Item.State }} - {{ .Item.CreatedAt }}</div>
</li>
{{end}}</ul>
{{else}}<p class="empty">Nothing needs attention.</p>
{{end}}
<script>
/* Answer controls exist for message items only, and the form is intercepted so
   a failed answer is shown rather than swallowed into a reload: a bare catch
   that reloads anyway reports a code fault as a connection problem. */
document.querySelectorAll('form.answer').forEach(function (form) {
  form.addEventListener('submit', function (event) {
    event.preventDefault();
    var button = event.submitter;
    var kind = button ? button.value : 'text';
    var value = '';
    if (kind === 'option') { value = button.getAttribute('data-value') || ''; }
    if (kind === 'text') { value = form.querySelector('input[name=text]').value; }
    var answer = { kind: kind };
    if (value) { answer.value = value; }
    fetch('/api/1.0/attention/' + encodeURIComponent(form.closest('li.item').getAttribute('data-item-id')) + '/answer', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ answered_by: 'attention-board', answer: answer })
    }).then(function (response) {
      if (response.ok) { window.location.reload(); return; }
      return response.text().then(function (body) {
        showNote(form, 'Answer failed - HTTP ' + response.status + ' - ' + body, true);
      });
    }).catch(function (error) {
      showNote(form, 'Answer failed - ' + String(error), true);
    });
  });
});
function showNote(form, message, isError) {
  var previous = form.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('div');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  form.appendChild(note);
}
/* The read-aloud control is type="button" so it never submits the answer form.
   It reports the tts message id rather than reloading, because the item is
   still open and the id is what a caller polls for playback status. */
document.querySelectorAll('button[data-speak]').forEach(function (button) {
  button.addEventListener('click', function () {
    var form = button.closest('form.answer');
    var itemID = form.closest('li.item').getAttribute('data-item-id');
    fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/speak', { method: 'POST' })
      .then(function (response) {
        return response.text().then(function (body) {
          showNote(
            form,
            response.ok
              ? 'Reading aloud (' + body + ')'
              : 'Read aloud failed - HTTP ' + response.status + ' - ' + body,
            !response.ok
          );
        });
      }).catch(function (error) {
        showNote(form, 'Read aloud failed - ' + String(error), true);
      });
  });
});
/* ⚠️ The Jump control is a button, not a link, and the difference is the
   operator's ask: "so we dont switch the screen". A link navigates the browser
   to the fleet-jump server, taking the board out of view. This calls the
   board's own endpoint, which performs the jump server-side and answers 204 —
   so a click switches WezTerm and leaves this page exactly where it was. */
document.querySelectorAll('button[data-jump]').forEach(function (button) {
  button.addEventListener('click', function () {
    var row = button.closest('li.item');
    fetch(button.getAttribute('data-jump'), { method: 'GET' })
      .then(function (response) {
        if (response.ok) { showJumpNote(row, 'Jumped.', false); return; }
        return response.text().then(function (body) {
          showJumpNote(row, 'Jump failed - HTTP ' + response.status + ' - ' + body, true);
        });
      }).catch(function (error) {
        showJumpNote(row, 'Jump failed - ' + String(error), true);
      });
  });
});
function showJumpNote(row, message, isError) {
  var container = row.querySelector('.jump');
  var previous = container.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('span');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  container.appendChild(note);
}
</script>
</body>
</html>
`

// attentionPageRow is one item paired with what could be resolved about its
// origin. Pairing here rather than in the template keeps the lookup out of the
// template language: a map indexed by item id inside `range` is exactly the
// kind of indirection html/template makes awkward, and it would put a silent
// zero-value Provenance one typo away from a rendered row.
type attentionPageRow struct {
	Item       pkg.Item
	Provenance pkg.Provenance
	// Message reports whether this row renders answer controls. True for
	// `message` items only: a `permission` item is approve-shaped and only the
	// operator may answer it in the session that raised it, so a control there
	// would be the permission laundering the schema forbids.
	Message bool
	// Jump is the copyable command handing a non-`message` item back to the
	// session that raised it. Empty when no pane resolved — an unresolvable
	// value renders absent rather than as a stand-in, per the schema's silence 7.
	//
	// It is empty for a `message` item as well, which renders the button without
	// the command: the div's label is false for an item the board may answer in
	// place, and the command text is redundant beside the button.
	Jump string
	// JumpURL is the board-relative path that hands this row back to its
	// session, rendered as the Jump button. Set for every class, `message`
	// included. Empty when no pane resolved, on the same rule as Jump.
	//
	// It is also empty when the token is unreadable, and ⚠️ that must degrade
	// the button *alone*: the template renders the div when either field is
	// set, so a host with no token still gets the copyable command exactly as
	// it rendered before this change. Gating the whole div on JumpURL would
	// silently drop the command too.
	JumpURL string
}

// attentionPageData is what the template renders: the items the store's read
// path returned, in the order it returned them. It carries no ordering of its
// own — the store never ranks, so the page must not either.
type attentionPageData struct {
	Items []attentionPageRow
	// Speak reports whether a read-aloud control should render. False when no
	// tts server is configured, so the page never offers a control whose
	// endpoint is unrouted.
	Speak bool
}

// jumpCommand renders the copyable half of the handover for an item the board
// must not answer.
//
// ⚠️ This was the *whole* handover, and a command only, on the reasoning that a
// browser cannot activate a WezTerm tab. That premise is false against the
// fleet-jump server, which activates a pane from a plain HTTP GET, so the row
// now carries jumpURL's button as well. The command stays because an operator
// may still want to paste it — it is added to, never replaced.
//
// An item whose pane did not resolve yields no command, so its row renders
// exactly as it did before: an unresolvable value renders absent rather than as
// a stand-in, which is what the schema's silence 7 forbids.
//
// It is empty for a `message` item, which renders the button without the
// command — the label above it is false for an item the board may answer in
// place.
func jumpCommand(item pkg.Item, provenance pkg.Provenance) string {
	if item.AnswerMechanism == pkg.MessageAnswerMechanism {
		return ""
	}
	if provenance.Pane == "" {
		return ""
	}
	return "/supervisor:jump " + provenance.Pane
}

// jumpURL renders the clickable half of the handover: a path on this board, not
// the fleet-jump server's URL.
//
// The fleet-jump URL carries the shared token as a query parameter, so putting
// it in the page would publish the token in the served document on every load.
// This path is called in the background and the board performs the jump
// server-side, which keeps the token on the server — and keeps the browser on
// the board, since a real link would navigate it away.
//
// Empty when no pane resolved, or when enabled is false — the same absence rule
// as jumpCommand, so a row with no resolvable pane, and a host with no readable
// token, each render no button and no placeholder.
func jumpURL(item pkg.Item, provenance pkg.Provenance, enabled bool) string {
	if !enabled {
		return ""
	}
	if provenance.Pane == "" {
		return ""
	}
	return "/jump/" + string(item.ItemID)
}

// NewAttentionPageHandler creates the read-only HTML page a human opens to see
// what currently needs attention.
//
// ⚠️ This handler reverses the contract it was built with, and the reversal is
// deliberate rather than drift. It previously performed **no render-time
// lookup at all**, on the reasoning that ProducerID is a session id, the
// registry deletes its entry when the session exits, and a render-time lookup
// would therefore find nothing and present an unresolvable value as resolved.
// That reasoning is still honoured — an unresolvable value renders absent, and
// a pane is shown only when it validates — but its blanket prohibition could
// not survive the operator's ask, which is precisely for values the store does
// not carry: the store's fifteen-field Item has no host, cwd, tool or pane.
//
// The join is best-effort and the page stays usable without it. Every source is
// read through a resolver that fails soft: a host with no WezTerm, a store with
// no Claude Code state directory, an unreadable registry — each resolves to no
// values for that row, and the page renders exactly what it rendered before.
// That is the honest scoping of this page's standalone claim: it still works
// without Claude Code, but it works *with provenance absent* rather than
// without looking.
//
// jumpTokens gates the Jump button. It is checked per render rather than once
// at construction, so a token removed while the service runs drops the button
// on the next load — a control whose endpoint would refuse is a value presented
// as working that is not.
func NewAttentionPageHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	jumpTokens pkg.JumpTokenReader,
) http.Handler {
	// Parsed once at construction rather than per request: the template is a
	// compile-time constant, so a parse failure is a programming error, and
	// template.Must makes it a startup failure rather than a per-request one.
	page := template.Must(template.New("attention-page").Parse(attentionPageTemplate))
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				items, err := store.Read(ctx)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "read failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				// Resolved for the whole page at once, so the host-wide reads
				// behind it (the pane listing, the session registry) happen once
				// rather than once per row.
				provenances := provenance.Resolve(ctx, items)
				// Read per render rather than once at construction, so a token
				// rotated or removed while the service runs is reflected on the
				// next load. An unreadable token renders no button rather than
				// failing the page — the same absent-not-placeholder rule the
				// provenance line follows.
				jumpEnabled := false
				if jumpTokens != nil {
					if _, err := jumpTokens.Read(ctx); err != nil {
						glog.V(3).
							Infof("jump token unavailable, rendering no jump buttons: %v", err)
					} else {
						jumpEnabled = true
					}
				}
				rows := make([]attentionPageRow, 0, len(items))
				for _, item := range items {
					resolved := provenances[item.ItemID]
					rows = append(rows, attentionPageRow{
						Item:       item,
						Provenance: resolved,
						Message:    item.AnswerMechanism == pkg.MessageAnswerMechanism,
						Jump:       jumpCommand(item, resolved),
						JumpURL:    jumpURL(item, resolved, jumpEnabled),
					})
				}
				// Rendered into a buffer first so a render failure can still
				// produce the standard JSON error body. Writing straight to the
				// response would commit a 200 and a partial document before the
				// error handler had a chance to report anything.
				var body bytes.Buffer
				if err := page.Execute(&body, attentionPageData{Items: rows, Speak: speakEnabled}); err != nil {
					return errors.Wrap(ctx, err, "render page failed")
				}
				resp.Header().Set(
					libhttp.ContentTypeHeaderName,
					libhttp.TextHTML+"; charset=utf-8",
				)
				if _, err := resp.Write(body.Bytes()); err != nil {
					return errors.Wrap(ctx, err, "write response failed")
				}
				return nil
			},
		),
	)
}
