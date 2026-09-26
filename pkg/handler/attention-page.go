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
// The card is shaped by the declared cardinality rather than by taste: a
// question that allows several picks renders checkboxes and one that allows a
// single pick renders radio buttons, both read from the producer's
// AnswerCardinality and never inferred from the option count — a one-option
// question and a many-option single-pick question carry lists of different
// lengths and ask for different things, so a board that read the length would
// render a checkbox for a question admitting one answer. An item carrying
// `questions` renders one tab per question, each with its own payload, options
// and control; a single-question item renders one card with no tab strip.
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
  /* The two green tokens are the only addition to the palette, and they exist
     for one control: Next is the forward move, so it reads as the affirmative
     one beside a muted Dismiss. They are tokens rather than literals so the
     pair stays one decision. */
  --green: #8fd39a;
  --green-bg: #2c4a37;
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
/* The answer card, rendered for message items only. The context line carries
   the background the producer declared, kept separate from the question so the
   ask stays readable on its own at the top of the row. */
.context { color: var(--muted); font-size: 13px; line-height: 1.45; margin: 0 0 8px; white-space: pre-wrap; }
/* One tab per question of a multi-question item. The active tab is outlined
   rather than filled, so the strip reads as a set of labels with one selected
   rather than as a row of buttons competing with Next. */
.tabs { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 20px; }
.tab {
  background: transparent;
  color: var(--muted);
  border: 1px solid transparent;
  border-radius: 9px;
  padding: 7px 15px;
  font-size: 14px;
  font-family: inherit;
  cursor: pointer;
}
.tab:hover { color: var(--text); }
.tab.active { color: var(--text); background: var(--bg); border-color: var(--muted); }
/* A multi-question item's own payload: the card's title rather than a question,
   so it is muted and the tabs carry the questions. */
.card-title { color: var(--muted); font-size: 15px; line-height: 1.45; margin: 0 0 16px; white-space: pre-wrap; }
/* The question line. The cardinality hint rides on it in the producer's own
   wording, so the operator reads what the control will accept before using it. */
.question { font-size: 17px; font-weight: 600; line-height: 1.4; margin: 0 0 20px; }
.question .hint { font-weight: 400; color: var(--muted); }
.options { display: flex; flex-direction: column; gap: 18px; margin: 0 0 22px; }
.option { display: flex; align-items: flex-start; gap: 12px; cursor: pointer; }
/* The control is aligned to the label's first line rather than to the row, so
   the label and its muted cost line read as one block beside it. */
.option input { flex: none; width: 16px; height: 16px; margin: 3px 0 0; accent-color: var(--muted); }
.option-body { flex: 1 1 auto; min-width: 0; }
.option-label { display: block; font-size: 15px; line-height: 1.4; }
.option-label .recommended { color: var(--muted); }
.option-desc { display: block; color: var(--muted); font-size: 14px; line-height: 1.45; margin-top: 4px; }
.other {
  width: 100%;
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 9px;
  padding: 13px 15px;
  font-size: 14px;
  font-family: inherit;
  margin: 0 0 20px;
}
.other::placeholder { color: var(--muted); }
.actions { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; }
.actions button {
  font-family: inherit;
  font-size: 14px;
  border: 1px solid var(--border);
  border-radius: 9px;
  padding: 9px 18px;
  cursor: pointer;
}
/* Dismiss is the skip, so it is muted and Next carries the colour: the one
   forward move should be the one that reads as the affirmative. */
.actions .dismiss { background: transparent; color: var(--muted); }
.actions .dismiss:hover { color: var(--text); border-color: var(--muted); }
.actions .next { background: var(--green-bg); color: var(--green); border-color: var(--green-bg); font-weight: 500; }
.actions .next:hover { border-color: var(--green); }
.actions .speak { background: transparent; color: var(--muted); }
.actions .speak:hover { color: var(--text); border-color: var(--muted); }
/* The acknowledge control is the only action a report-only card carries, so it
   takes the affirmative colour the message card gives Next: on a card that
   offers no other move, it is the forward one. */
.actions .ack { background: var(--green-bg); color: var(--green); border-color: var(--green-bg); font-weight: 500; }
.actions .ack:hover { border-color: var(--green); }
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
{{range .Items}}{{template "attention-row" .}}{{end}}</ul>
{{else}}<p class="empty">Nothing needs attention.</p>
{{end}}
<script>
/* Tabs switch which question panel is visible. Nothing reloads and no panel is
   re-rendered: every panel ships in the document and only its visibility
   changes, so a half-typed Other field survives a look at the other question. */
document.querySelectorAll('.tabs').forEach(function (tabs) {
  var row = tabs.closest('li.item');
  tabs.querySelectorAll('button[data-tab]').forEach(function (tab) {
    tab.addEventListener('click', function () {
      tabs.querySelectorAll('button[data-tab]').forEach(function (other) {
        other.classList.toggle('active', other === tab);
      });
      row.querySelectorAll('.panel').forEach(function (panel) {
        panel.hidden = panel.getAttribute('data-question') !== tab.getAttribute('data-tab');
      });
    });
  });
});
/* Answer controls exist for message items only, and the form is intercepted so
   a failed answer is shown rather than swallowed into a reload: a bare catch
   that reloads anyway reports a code fault as a connection problem. */
document.querySelectorAll('form.answer').forEach(function (form) {
  form.addEventListener('submit', function (event) {
    event.preventDefault();
    var button = event.submitter;
    var kind = button ? button.value : 'send';
    var multi = form.getAttribute('data-multi') === 'true';
    /* automation carries the page's own navigator.webdriver reading, which only
       this script can read. It is the one answered-client member the body may
       carry and explicitly the weaker one; user_agent and remote_addr are read
       by the store from the request and a body cannot set either. It is omitted
       rather than sent as false when the browser does not report it. */
    var request = { answered_by: 'attention-board', automation: navigator.webdriver };
    if (kind === 'skip') {
      /* Dismiss declines the whole card, so a multi-question item records a
         declined answer on every tab rather than on none. */
      if (multi) { request.answers = skipAll(form); } else { request.answer = { kind: 'skip' }; }
      sendAnswer(form, request);
      return;
    }
    var entries = collectAnswers(form);
    if (!entries.length) {
      showNote(form, 'Pick an option or write an answer first.', true);
      return;
    }
    if (multi) { request.answers = entries; } else { request.answer = singleAnswer(entries[0]); }
    sendAnswer(form, request);
  });
});
/* singleAnswer maps one collected entry onto the item-level answer shape.
   It carries values when the question took several picks: a single-question item
   declared multiple has no other field for them, so sending only value would
   drop every pick but the first, and the store rejects the resulting body. */
function singleAnswer(entry) {
  var answer = { kind: entry.kind };
  if (entry.values) { answer.values = entry.values; }
  else if (entry.value) { answer.value = entry.value; }
  return answer;
}
/* collectAnswers reads one entry per question the operator actually answered. A
   question left alone contributes nothing: an entry there would read back as a
   value where the operator gave none. */
function collectAnswers(form) {
  var entries = [];
  form.querySelectorAll('.panel').forEach(function (panel) {
    var question = panel.getAttribute('data-question') || '';
    var picks = [];
    panel.querySelectorAll('input:checked').forEach(function (input) {
      picks.push(input.getAttribute('data-option') || '');
    });
    var other = panel.querySelector('input[name=text]');
    var text = other ? other.value.trim() : '';
    if (text) {
      entries.push({ question: question, kind: 'text', value: text });
      return;
    }
    if (!picks.length) { return; }
    /* The carrier is fixed by the question's declared cardinality, not chosen
       here: a single-pick question carries value, a multi-pick one carries
       values. Joining the picks into one string would be lossy — a label
       containing ", " would read back as two picks — and the store rejects an
       entry using the field its question's cardinality does not name. */
    if (panel.getAttribute('data-multi-pick') === 'true') {
      entries.push({ question: question, kind: 'option', values: picks });
    } else {
      entries.push({ question: question, kind: 'option', value: picks[0] });
    }
  });
  return entries;
}
function skipAll(form) {
  var entries = [];
  form.querySelectorAll('.panel').forEach(function (panel) {
    entries.push({ question: panel.getAttribute('data-question') || '', kind: 'skip' });
  });
  return entries;
}
function sendAnswer(form, request) {
  fetch('/api/1.0/attention/' + encodeURIComponent(form.closest('li.item').getAttribute('data-item-id')) + '/answer', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(request)
  }).then(function (response) {
    if (response.ok) { showNote(form, 'Answer sent.', false); return; }
    return response.text().then(function (body) {
      var failure = answerFailure(body);
      if (failure.leftQueue) {
        /* The item left the queue between this page being drawn and this answer
           arriving — a lost race against the producer's exit, not a malformed
           request. The line is shown AND the page returns to the queue, in that
           order and with a beat between them. Reloading first would swallow the
           outcome into a reload, which the rule above forbids; staying put would
           leave a card in front of the operator for an item that no longer
           exists, which is the defect this branch exists to fix. Both halves
           are the point, so neither is dropped. */
        showNote(form, failure.message, true);
        window.setTimeout(function () { window.location.reload(); }, 2500);
        return;
      }
      showNote(form, 'Answer failed - HTTP ' + response.status + ' - ' + body, true);
    });
  }).catch(function (error) {
    showNote(form, 'Answer failed - ' + String(error), true);
  });
}
/* answerFailure reads the store's error envelope and reports whether the item
   had already left the queue. Only that one code is special-cased: every other
   failure keeps the raw body, because the body is what a reader needs to tell a
   code fault from a connection problem. A body that is not JSON at all — a
   proxy's error page, a truncated response — reads as "not this case" rather
   than throwing, so the caller still reaches its raw-body line. */
function answerFailure(body) {
  var envelope;
  try { envelope = JSON.parse(body); } catch (error) { return { leftQueue: false }; }
  var failure = envelope && envelope.error;
  if (!failure || failure.code !== 'ITEM_CLOSED') { return { leftQueue: false }; }
  var closedAt = (failure.details && failure.details.closed_at) || '';
  return {
    leftQueue: true,
    message: closedAt
      ? 'This item was already closed at ' + closedAt + ' - it left the queue before this answer arrived. Returning to the queue.'
      : 'This item had already left the queue before this answer arrived. Returning to the queue.'
  };
}
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
/* ⚠️ The acknowledge control closes the item — open -> closed — and it is the
   only control a report-only card carries. It posts the board as the arm that
   caused the close, which is what lets a reader tell a board acknowledgement
   from a producer withdrawing its own item or from the store's producer-exit
   sweep: neither of those records an answered_by at all. answered_at stays
   unset, because an ack item routes nothing back to its producer. */
document.querySelectorAll('button[data-ack]').forEach(function (button) {
  button.addEventListener('click', function () {
    var row = button.closest('li.item');
    fetch('/api/1.0/attention/' + encodeURIComponent(row.getAttribute('data-item-id')) + '/close', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      /* automation is the page's own navigator.webdriver reading — the one
         answered-client member the body may carry; the store reads user_agent
         and remote_addr from the request itself. */
      body: JSON.stringify({ answered_by: 'attention-board', automation: navigator.webdriver })
    }).then(function (response) {
      if (response.ok) { showAckNote(row, 'Acknowledged.', false); return; }
      return response.text().then(function (body) {
        showAckNote(row, 'Acknowledge failed - HTTP ' + response.status + ' - ' + body, true);
      });
    }).catch(function (error) {
      showAckNote(row, 'Acknowledge failed - ' + String(error), true);
    });
  });
});
function showAckNote(row, message, isError) {
  var container = row.querySelector('.actions');
  var previous = container.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('span');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  container.appendChild(note);
}
/* The live channel. The page subscribes once and swaps rows in place as the
   store changes, so an open board tracks the store instead of freezing at load.
   The stream carries a RENDERED ROW rather than an item: the markup comes from
   the server's own template, so there is one renderer and a row arriving here
   cannot drift from the same row on a fresh load — which is also why this stays
   plain DOM work with no framework and no build step.

   Nothing reloads, here or after an action. An earlier version reloaded the
   page once an answer was accepted; that is exactly the manual step this board
   exists to remove, and it would also defeat the channel's own negative control
   — with the stream blocked, a reload would make the row change anyway, and the
   control could not then tell the channel from a coincidence. */
(function () {
  if (typeof EventSource === 'undefined') { return; }
  function rowFor(itemID) {
    /* Compared rather than selected: the id lands in a selector string
       otherwise, and a generated id is not the place to rely on. */
    var rows = document.querySelectorAll('li.item');
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute('data-item-id') === itemID) { return rows[i]; }
    }
    return null;
  }
  function collapseIfEmpty() {
    var list = document.querySelector('ul.items');
    if (!list || list.querySelector('li.item')) { return; }
    var empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = 'Nothing needs attention.';
    list.parentNode.replaceChild(empty, list);
  }
  function upsertRow(itemID, html) {
    var row = rowFor(itemID);
    if (row) { row.outerHTML = html; return; }
    var list = document.querySelector('ul.items');
    if (!list) {
      /* The board rendered its empty state, so the list does not exist yet.
         The new list takes that paragraph's place rather than being appended,
         so a row arriving into an empty board lands where a row belongs. */
      var empty = document.querySelector('p.empty');
      if (!empty) { return; }
      list = document.createElement('ul');
      list.className = 'items';
      empty.parentNode.replaceChild(list, empty);
    }
    list.insertAdjacentHTML('beforeend', html);
  }
  var source = new EventSource('/api/1.0/attention/stream');
  /* No reconnect handler: EventSource reconnects on its own, which is the
     property that lets the board survive a restart of the store. */
  source.onmessage = function (event) {
    var change = JSON.parse(event.data);
    if (change.type === 'remove') {
      var row = rowFor(change.item_id);
      if (row) { row.remove(); }
      collapseIfEmpty();
      return;
    }
    upsertRow(change.item_id, change.html);
  };
})();
</script>
</body>
</html>
{{/* One item row, as a sub-template rather than inline in the list. The live
     stream sends a changed row to the open page as rendered HTML and the page
     swaps that node in place, so the row markup has to be renderable on its own
     — and it must be the SAME markup, not a second renderer that would drift
     from this one. The dollar sign inside a sub-template is the value passed to
     it, which is why Speak is carried on the row and read as dot-Speak here. */}}
{{define "attention-row"}}<li class="item" data-item-id="{{ .Item.ItemID }}">
<div class="producer">{{ .Item.ProducerID }} ({{ .Item.ProducerKind }})</div>
{{if not .Message}}<div class="payload">{{ .Item.Payload }}</div>
{{end}}{{if .Item.Context}}<div class="context">{{ .Item.Context }}</div>
{{end}}{{if .Provenance.Resolved}}<div class="provenance">{{if .Provenance.Host}}<span class="host">{{ .Provenance.Host }}</span>{{end}}{{if .Provenance.Cwd}}<span class="cwd">{{ .Provenance.Cwd }}</span>{{end}}{{if .Provenance.Tool}}<span class="tool">{{ .Provenance.Tool }}</span>{{end}}{{if .Provenance.Pane}}<span class="pane">pane {{ .Provenance.Pane }}</span>{{else if .Provenance.PaneRecorded}}<span class="unroutable">unroutable</span>{{end}}</div>
{{end}}{{if .Message}}<form class="answer" data-multi="{{ .Tabs }}">
{{if .Tabs}}<div class="tabs">{{range .Questions}}<button type="button" class="tab{{if .Active}} active{{end}}" data-tab="{{ .Tab }}">{{ .Tab }}</button>{{end}}</div>
<div class="card-title">{{ .Item.Payload }}</div>
{{end}}{{range .Questions}}{{$question := .}}<div class="panel" data-question="{{ $question.Tab }}" data-multi-pick="{{ $question.Multi }}"{{if not $question.Active}} hidden{{end}}>
<div class="question">{{ $question.Payload }}{{if $question.Hint}} <span class="hint">({{ $question.Hint }})</span>{{end}}</div>
{{if $question.Options}}<div class="options">
{{range $question.Options}}<label class="option"><input type="{{ if $question.Multi }}checkbox{{ else }}radio{{ end }}" name="{{ $question.Name }}" value="{{ .Label }}" data-option="{{ .Label }}"><span class="option-body"><span class="option-label">{{ .Label }}{{if .Recommended}} <span class="recommended">(Recommended)</span>{{end}}</span>{{if .Description}}<span class="option-desc">{{ .Description }}</span>{{end}}</span></label>
{{end}}</div>
{{end}}<input class="other" type="text" name="text" placeholder="Other...">
</div>
{{end}}<div class="actions"><button type="submit" name="kind" value="skip" class="dismiss">✕ Dismiss</button><button type="submit" name="kind" value="send" class="next">✓ Next</button>{{if .Speak}}<button type="button" class="speak" data-speak>Read aloud</button>{{end}}</div>
</form>
{{end}}{{if .Ack}}<div class="actions"><button type="button" class="ack" data-ack>Acknowledge</button></div>
{{end}}{{if or .Jump .JumpURL}}<div class="jump">{{if .Jump}}<span>Approve in the session that asked: <code>{{ .Jump }}</code></span>{{end}}{{if .JumpURL}}<button type="button" class="jump-button" data-jump="{{ .JumpURL }}">Jump to session</button>{{end}}</div>
{{end}}<div class="meta">{{ .Item.State }} - {{ .Item.CreatedAt }}</div>
</li>{{end}}
`

// attentionPageQuestion is one question unit as the card renders it: the unit a
// tab selects and a panel shows. A single-question item renders exactly one of
// these, built from the item's own Payload, Options and AnswerCardinality, so
// the template has one panel shape to render rather than two.
type attentionPageQuestion struct {
	// Tab is the tab label, and the key an answer names. Empty on a
	// single-question item, which renders no tab strip.
	Tab string
	// Payload is the question itself.
	Payload pkg.Payload
	// Hint is the cardinality hint appended to the question line in the
	// producer's own wording, e.g. "pick any number". Empty when the question
	// offers no options, where a statement about picks would describe a choice
	// the question does not offer.
	Hint string
	// Multi reports whether the question takes several picks. It selects the
	// control — a checkbox when true, a radio button when false — and is read
	// from the declared cardinality, never from the option count.
	Multi bool
	// Active marks the question whose panel renders open. Exactly one carries it,
	// which is what the tab strip and the panels agree on before any click.
	Active bool
	// Name is the input group name for this question's controls, scoped to the
	// item as well as to the question so two cards on one page cannot share a
	// radio group — a shared name would let a pick on one card clear another's.
	Name string
	// Options are this question's choices, in the order the producer declared
	// them. They are the schema's own type rather than a mirror of it, exactly as
	// Provenance is, so the card cannot drift from the field it renders.
	Options pkg.AnswerOptions
}

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
	// Ack reports whether this row renders the acknowledge control. True for
	// `ack` items only.
	//
	// ⚠️ This is the second half of an affordance that used to be a single
	// boolean, and the gap it closes was not cosmetic. An `ack` item is a
	// condition report: it asks nothing and routes nothing back, so the schema
	// closes it by acknowledgement — `open` → `closed` with `answered_at`
	// unset — and names an arm as a causer of that row. With no branch for it,
	// an `ack` row fell through the same `not .Message` path as a `permission`
	// row and rendered its payload and a state line and nothing else, so it sat
	// on the only surface that showed it and could not be cleared from there.
	//
	// A `permission` row is deliberately still in that position: it is
	// approve-shaped, only the operator may answer it in the session that
	// raised it, and a control here would be the permission laundering the
	// schema forbids. The two classes rendering identically was the accident;
	// they are separated by this field rather than by a shared absence.
	Ack bool
	// Questions are the question units this row's card renders: the item's own
	// when it carries several, otherwise a single unit built from the item's own
	// Payload, Options and AnswerCardinality. Empty when the row renders no card,
	// which is what keeps the template's card a single `if .Message` away from a
	// `permission` row rather than a second thing to remember.
	//
	// Precomputed here for the reason Provenance is: html/template cannot index a
	// map inside `range`, and a zero-value question one typo away would render a
	// card with no controls at all.
	Questions []attentionPageQuestion
	// Tabs reports whether the card renders a tab strip — true when the item
	// carries several questions. It is the flag the template reads and the flag
	// the page's own script reads, so the strip and the wire shape cannot
	// disagree about whether this item answers by `answer` or by `answers`.
	Tabs bool
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
	// Speak reports whether this row renders the read-aloud control. It is
	// carried on the row rather than read from the page root because the row is
	// a sub-template: `{{template "attention-row" .}}` passes the row as the
	// data, so `$` inside it is the row and not the page, and a `$.Speak` left
	// in place would resolve against the wrong value.
	Speak bool
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

// newAttentionPageRow pairs an item with what could be resolved about its origin
// and precomputes the question units its card renders.
//
// The question units are built only for a `message` item. A `permission` item
// renders no card, so building units it would never render would be a value
// carried for nothing — and, worse, one a later change could render by
// accident.
func newAttentionPageRow(
	item pkg.Item,
	provenance pkg.Provenance,
	jumpEnabled bool,
	speak bool,
) attentionPageRow {
	row := attentionPageRow{
		Item:       item,
		Provenance: provenance,
		Message:    item.AnswerMechanism == pkg.MessageAnswerMechanism,
		Ack:        item.AnswerMechanism == pkg.AckAnswerMechanism,
		Jump:       jumpCommand(item, provenance),
		JumpURL:    jumpURL(item, provenance, jumpEnabled),
		Speak:      speak,
	}
	if row.Message {
		row.Questions = pageQuestions(item)
		row.Tabs = len(item.Questions) > 0
	}
	return row
}

// pageQuestions builds the question units a row's card renders. An item carrying
// `questions` renders one unit per question in the declared order; a
// single-question item renders exactly one, built from the item's own Payload,
// Options and AnswerCardinality — which is what makes the template's panel loop
// the only rendering path rather than one of two.
//
// The first unit is the active one, so the strip and the panels agree on which
// question is open before any click.
func pageQuestions(item pkg.Item) []attentionPageQuestion {
	if len(item.Questions) == 0 {
		return []attentionPageQuestion{
			{
				Payload: item.Payload,
				Hint:    cardinalityHint(item.AnswerCardinality, len(item.Options)),
				Multi:   item.AnswerCardinality == pkg.MultipleAnswerCardinality,
				Active:  true,
				Name:    optionName(item.ItemID, ""),
				Options: item.Options,
			},
		}
	}
	questions := make([]attentionPageQuestion, 0, len(item.Questions))
	for index, question := range item.Questions {
		questions = append(questions, attentionPageQuestion{
			Tab:     question.Tab,
			Payload: question.Payload,
			Hint:    cardinalityHint(question.Cardinality, len(question.Options)),
			Multi:   question.Cardinality == pkg.MultipleAnswerCardinality,
			Active:  index == 0,
			Name:    optionName(item.ItemID, question.Tab),
			Options: question.Options,
		})
	}
	return questions
}

// cardinalityHint renders the parenthetical that rides the question line, in the
// producer's own wording rather than as a machine value.
//
// An absent cardinality reads as single, exactly as the schema says it does, so
// a pre-change item renders the single-pick hint rather than none — the control
// it gets is a radio button, and a hint agreeing with the control is what makes
// the card self-explaining. A question offering no options renders no hint at
// all: a statement about picks would describe a choice the question does not
// offer.
func cardinalityHint(cardinality pkg.AnswerCardinality, optionCount int) string {
	if optionCount == 0 {
		return ""
	}
	if cardinality == pkg.MultipleAnswerCardinality {
		return "pick any number"
	}
	return "pick one"
}

// optionName is the input group name for one question's controls. It carries the
// item id as well as the tab so two cards on one page cannot share a radio
// group: a shared name would let a pick on one card clear another's.
func optionName(itemID pkg.ItemID, tab string) string {
	return "option-" + itemID.String() + "-" + tab
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
					rows = append(
						rows,
						newAttentionPageRow(
							item,
							provenances[item.ItemID],
							jumpEnabled,
							speakEnabled,
						),
					)
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
