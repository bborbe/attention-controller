
/* A throw outside the four handled paths — a listener that dereferences a node
   the stream replaced, a promise chain someone adds later without a catch —
   reaches the console only if devtools happens to be open when it fires. These
   two handlers write it there unconditionally, so a card that misbehaves leaves
   a trail the operator can read back afterwards.
   They log and nothing else. An uncaught error has no form and no row to host a
   note, so surfacing one on the page would be a new visible element on a path
   that renders none today. Registered at the top level rather than inside a
   handler, so a throw before the first click is caught too.
   window.onerror is given (message, source, lineno, colno, error); the error
   object is preferred where the browser supplies one and the message is the
   fallback, because the object carries the stack the message alone loses. */
window.onerror = function (message, source, lineno, colno, error) {
  console.error(error || message);
};
/* Every promise chain in this script carries its own .catch, so nothing escapes
   to this handler today. It is registered for parity with window.onerror rather
   than because a rejection currently reaches it — code added later may reject
   without one, and a silent rejection is the failure this pair exists to end. */
window.addEventListener('unhandledrejection', function (event) {
  console.error(event.reason);
});
/* Tabs switch which question panel is visible. Nothing reloads and no panel is
   re-rendered: every panel ships in the document and only its visibility
   changes, so a half-typed Other field survives a look at the other question.
   ⚠️ Delegated on the document, not bound per button — the defect and the fix
   are the read-aloud control's below; this strip was left on the old binding and
   kept rendering while doing nothing after one row re-render. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var tab = event.target.closest('button[data-tab]');
  if (!tab) { return; }
  var tabs = tab.closest('.tabs');
  var row = tabs && tabs.closest('li.item');
  if (!row) { return; }
  tabs.querySelectorAll('button[data-tab]').forEach(function (other) {
    other.classList.toggle('active', other === tab);
  });
  row.querySelectorAll('.panel').forEach(function (panel) {
    panel.hidden = panel.getAttribute('data-question') !== tab.getAttribute('data-tab');
  });
});
/* Typing in Other selects the Other option and clears every other pick on that
   card, so the state on screen is the state that will be sent. Both halves are
   load-bearing. Selecting it is what makes the pick visible at all: without it
   the field sits beside a still-filled radio that collectAnswers will discard
   the moment text exists. Clearing the rest is what keeps a multi-pick card
   honest — an answer is option OR text, so boxes left checked beside typed text
   would read as picks that are never sent, which is the defect itself.
   Emptying the field releases the pick rather than restoring an earlier one:
   the answer reverts to unanswered, which is what the operator sees.
   ⚠️ Delegated on the document for the tab strip's reason: a row re-render
   replaces the field, and a listener bound to the replaced node goes silent
   while the control still draws. */
document.addEventListener('input', function (event) {
  var field = event.target;
  if (!field || !field.classList || !field.classList.contains('other')) { return; }
  var panel = field.closest('.panel');
  var pick = panel && panel.querySelector('input[data-other]');
  if (!pick) { return; }
  if (!field.value.trim()) { pick.checked = false; return; }
  pick.checked = true;
  panel.querySelectorAll('input:checked').forEach(function (input) {
    if (input !== pick) { input.checked = false; }
  });
});
/* The Other control's checked state is owned by the text field, never by a click
   anywhere on the option. Two states a click could reach are ones the submission
   would drop: checked with an empty field (collectAnswers skips the control's own
   value, so nothing is sent), and unchecked with text still in the field (the
   text is sent, so the control would read as released while the answer stands).
   Either one is the shown-pick-versus-sent-pick defect this control exists to
   remove — and on a single-pick card the click would additionally take the
   operator's earlier pick with it, because a radio group releases the previous
   selection the moment another is checked, with nothing to restore it.
   ⚠️ preventDefault on the click, rather than a change handler that reverts: a
   revert runs AFTER the browser has already toggled the control and, on a radio,
   already released the previous pick — the damage is done and there is nothing
   left to undo it with. Preventing the default means the toggle never happens.
   ⚠️ The guard matches the enclosing .option-other, never the control's own
   attribute. The option renders as a <label> wrapping BOTH the control and the
   Other… caption, and the caption is the wide target an operator actually aims
   at; a click there has the caption span as its target, so a guard testing only
   data-other returns early and leaves the label's own activation to check the
   control — the whole defect, on the larger half of the target.
   ⚠️ The field itself is the one click that must pass through untouched:
   preventDefault there would cancel the focus the field is about to take. */
document.addEventListener('click', function (event) {
  var target = event.target;
  if (!target || !target.closest) { return; }
  if (target.classList && target.classList.contains('other')) { return; }
  var panel = target.closest('.panel');
  if (!panel) { return; }
  var field = panel.querySelector('input.other');
  var pick = panel.querySelector('input[data-other]');
  if (target.closest('.option-other')) {
    event.preventDefault();
    if (field) { field.focus(); }
    return;
  }
  /* ⚠️ The mirror order: text first, then a named option. Picking one releases
     the Other control — the browser does that for a radio group, and the line
     below does it for the checkbox — but neither touches the FIELD, and
     collectAnswers reads text ahead of any pick. Left alone, the board would
     draw the option just clicked while the submission carried the text the
     operator had moved on from: the same shown-versus-sent defect, reached from
     the other side. The text goes with the control, because the text WAS that
     option's answer. */
  if (target.closest('.option') && field && field.value) {
    field.value = '';
    if (pick) { pick.checked = false; }
  }
});
/* Enter in the Other… field is the operator's ordinary way to submit, and it
   must send the answer rather than the card's first button.
   ⚠️ Load bearing: HTML implicit submission fires the form's DEFAULT button —
   the first submit button in tree order — and that is ✕ Dismiss
   (value="skip"), not ✓ Submit answer. So Enter on a card with text typed
   recorded {kind:'skip'} and the asking session received a DECLINE while the
   operator saw a successful submit. Measured on the live board 2026-11-12 with
   the same text on two cards: Enter → {"kind":"skip"}, click →
   {"kind":"text","value":"click"}. That is the worst shape this board can
   produce, because a session that trusts it acts on a decline nobody gave.
   ⚠️ requestSubmit(next), never form.submit(): form.submit() bypasses the submit
   event entirely, so the handler below would never run and nothing would be
   sent. requestSubmit dispatches a real submit event carrying button.next as
   the submitter, so event.submitter is the button whose value is 'send'.
   ⚠️ Reordering the buttons was the rejected alternative: making ✓ Submit
   answer first would fix Enter, but the row's existing specs and the operator's
   reading of the card both put Dismiss first, and a reorder fixes only the
   default-button case while this handler states the intent explicitly. */
document.addEventListener('keydown', function (event) {
  /* An IME composition confirm is a keydown too. Treating it as an answer
     submits a partial word: preventDefault swallows the confirm and
     requestSubmit takes the card off the board mid-word, so the operator can
     neither finish typing nor correct it. Same shown-versus-sent class as the
     Dismiss defect above, reached from the other side.
     ⚠️ Return BEFORE preventDefault. A guard that calls preventDefault and then
     returns still eats the keystroke — the card stays open but the composition
     is gone, which is the harder failure to notice.
     ⚠️ keyCode 229 is the only signal some Safari/iOS and Android IMEs give;
     isComposing alone misses them. */
  if (event.isComposing || event.keyCode === 229) { return; }
  if (event.key !== 'Enter') { return; }
  var field = event.target;
  if (!field || !field.classList || !field.classList.contains('other')) { return; }
  var form = field.closest('form.answer');
  if (!form) { return; }
  var next = form.querySelector('button.next');
  if (!next) { return; }
  event.preventDefault();
  form.requestSubmit(next);
});
/* Answer controls exist for message items only, and the form is intercepted so
   a failed answer is shown rather than swallowed into a reload: a bare catch
   that reloads anyway reports a code fault as a connection problem.
   ⚠️ Delegated on the document for the tab strip's reason, and measured too: on
   a re-rendered row Dismiss left the item open while the card still drew its
   buttons. A submit event bubbles, so the document is a valid host for it. */
document.addEventListener('submit', function (event) {
  var form = event.target && event.target.closest ? event.target.closest('form.answer') : null;
  if (!form) { return; }
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
    if (multi) { request.answers = kindAll(form, 'skip'); } else { request.answer = { kind: 'skip' }; }
    sendAnswer(form, request);
    return;
  }
  if (kind === 'hidden') {
    /* The corner X: the operator handled this card elsewhere — usually by
       answering in the asking session's own pane — and only wants it off the
       board. It records the kind and delivers nothing, which is the whole
       difference from Dismiss directly above: a decline would tell the asking
       session the opposite of what happened. */
    if (multi) { request.answers = kindAll(form, 'hidden'); } else { request.answer = { kind: 'hidden' }; }
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
/* The flag control is its OWN form with its OWN listener. ⚠️ Load bearing: the
   answer listener above resolves form.answer and would preventDefault, find no
   answer entries, and dispatch a 'send' answer — answering the card instead of
   flagging it. A flag form is not an answer form, and this listener is the only
   one that handles it. It is a top-level listener rather than part of the
   filter IIFE, which returns early when EventSource is undefined; the flag form
   is plain DOM work. The row's new state arrives over the SSE stream exactly as
   an answer's does, so this makes no request to /answer. */
document.addEventListener('submit', function (event) {
  var form = event.target && event.target.closest ? event.target.closest('form[data-flag-control]') : null;
  if (!form) { return; }
  event.preventDefault();
  var row = form.closest('li.item');
  if (!row) { return; }
  var field = form.querySelector('input[name=reason]');
  var reason = field ? field.value.trim() : '';
  if (!reason) {
    showNote(form, 'Say why this card should not have reached you.', true);
    return;
  }
  var itemID = row.getAttribute('data-item-id');
  /* Cleared at SUBMIT, not on success. ⚠️ Load bearing: the flag's stream echo
     can land before the fetch resolves, and the row swap carries unsent input
     onto the new node (carryFormState) — a field still holding the sent reason
     would be carried, and a success handler clearing the captured field would
     only empty the detached old node. On failure the reason is put back into
     whichever node is live by then. */
  field.value = '';
  fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/flag', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason: reason })
  }).then(function (response) {
    if (!response.ok) { throw new Error('flag failed: ' + response.status); }
    return response.json();
  }).catch(function (err) {
    var target = field;
    document.querySelectorAll('li.item').forEach(function (candidate) {
      if (candidate.getAttribute('data-item-id') !== itemID) { return; }
      var live = candidate.querySelector('form[data-flag-control] input[name=reason]');
      if (live) { target = live; }
    });
    if (!target.value) { target.value = reason; }
    showNote(target.closest('form') || form, 'Could not flag this card: ' + err.message, true);
  });
});
/* Unflag is reachable from the card the flag was written on, and that is the
   only place it can be reached: a flagged card is parked by the default view,
   so an operator who flagged one by mistake would otherwise have to know both
   the flags=none URL and the item id to undo it. It is a plain button rather
   than a form — the request carries no fields — and it is delegated on the
   document for the same reason the Acknowledge control is: a stream re-render
   replaces the row's whole node and takes any per-button listener with it. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-unflag]');
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  event.preventDefault();
  fetch('/api/1.0/attention/' + encodeURIComponent(row.getAttribute('data-item-id')) + '/unflag', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({})
  }).then(function (response) {
    if (!response.ok) { throw new Error('unflag failed: ' + response.status); }
    return response.json();
  }).catch(function (err) {
    showNote(row, 'Could not unflag this card: ' + err.message, true);
  });
});
/* The 👍/👎 rating. Clicking the highlighted icon clears it; clicking the
   other switches it. It posts to /rate only — never /answer — and changes no
   row state beyond the highlight; the stored value comes back over the SSE
   stream like any other row change, and the optimistic update below covers the
   gap. Delegated on the document because a stream re-render replaces the row. */
function applyRating(control, rating) {
  control.setAttribute('data-rating', rating);
  var buttons = control.querySelectorAll('button[data-rate]');
  for (var i = 0; i < buttons.length; i++) {
    var on = buttons[i].getAttribute('data-rate') === rating;
    buttons[i].classList.toggle('rated', on);
    buttons[i].setAttribute('aria-pressed', on ? 'true' : 'false');
  }
}
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-rate]');
  if (!button) { return; }
  var control = button.closest('[data-rate-control]');
  var row = button.closest('li.item');
  if (!control || !row) { return; }
  event.preventDefault();
  var previous = control.getAttribute('data-rating') || '';
  var clicked = button.getAttribute('data-rate');
  var next = previous === clicked ? '' : clicked;
  applyRating(control, next);
  fetch('/api/1.0/attention/' + encodeURIComponent(row.getAttribute('data-item-id')) + '/rate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ rating: next })
  }).then(function (response) {
    if (!response.ok) { throw new Error('rate failed: ' + response.status); }
    return response.json();
  }).catch(function (err) {
    applyRating(control, previous);
    showNote(row, 'Could not rate this card: ' + err.message, true);
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
      /* The Other control is a group member so that picking it unchecks the
         rest, but it carries no option: what it stands for is the text field's
         value, read above. Counting it here would send a pick the operator
         never made, and one the schema has no room for beside that text. */
      if (input.hasAttribute('data-other')) { return; }
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
/* The exclusive option is the do-nothing escape of a combinable menu
   ("nothing — close"): it and a real pick cannot both be submitted, because the
   pair describes a contradiction the asking session has no rule for. The server
   renders the marker; the rule lives here because it is a property of the
   interaction rather than of the markup, and it covers the Other control too —
   a typed answer beside "nothing" is the same contradiction a pick beside it
   would be.

   ⚠️ TWO directions, not one. Checking the escape clears every other box;
   checking any OTHER option clears only the escape. The rest of the menu stays
   combinable, which is the whole point of a multi-pick card. Clearing every
   other box on ANY check was the first version, and it made a card carrying an
   escape single-pick — the operator could not tick both commands, which is the
   defect this card shape exists to remove. Measured 2026-10-20 on the live
   board: with that rule, checking reflect then self-improve left only the
   second checked. */
document.addEventListener('change', function (event) {
  var input = event.target;
  if (!input || input.type !== 'checkbox' || !input.closest) { return; }
  var panel = input.closest('.panel');
  if (!panel || !input.checked) { return; }
  var isEscape = input.hasAttribute('data-exclusive');
  panel.querySelectorAll('input[type=checkbox]').forEach(function (box) {
    if (box === input) { return; }
    if (isEscape || box.hasAttribute('data-exclusive')) { box.checked = false; }
  });
});
/* A permission card's Allow / Deny. It writes the verdict as decision, the
   field the schema gives a permission answer; the row updates over the stream
   like every other answer, and a failure is shown on the card rather than
   swallowed. Delegated on the document for the tab strip's reason: a row the
   stream re-renders keeps working. */
document.addEventListener('click', function (event) {
  var button = event.target && event.target.closest ? event.target.closest('button[data-decision]') : null;
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  button.disabled = true;
  fetch('/api/1.0/attention/' + encodeURIComponent(row.getAttribute('data-item-id')) + '/answer', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ answered_by: 'attention-board', automation: navigator.webdriver, decision: button.getAttribute('data-decision') })
  }).then(function (response) {
    if (response.ok) { return; }
    return response.text().then(function (body) {
      button.disabled = false;
      /* ⚠️ The branch, not the envelope. answerFailure returns an OBJECT whose
         message is present only on the stale classification, and showCloseNote's
         contract is a STRING: passing the object straight through made
         note.textContent coerce it, so the card rendered the literal
         [object Object] for every code and dropped the store's own code and
         message along with it. rememberFailure persists whatever it is handed
         and replayFailure re-renders it, so the object survived the row swap as
         well — the fix has to produce a string HERE, not guard the coercion
         downstream. (Backticks are unavailable in this comment: the whole page
         is one Go raw-string literal.) */
      var failure = answerFailure(body);
      if (failure.staleCard) {
        /* The card went stale between this page being drawn and this answer
           arriving — either the item left the queue, or another arm answered it
           first. Neither is a malformed request, and in both the operator's
           click carried an outcome the raw body buries, so the line the code
           earns is shown AND the page returns to the queue, in that order and
           with a beat between them — the form path's order, for the form path's
           reason.

           ⚠️ The reload is load bearing here, and the reason is an error path.
           notifyingAttentionStore.Answer returns BEFORE its Notify when the
           answer fails, so a REJECTED answer publishes no change and the stream
           sends no delta: the row is never swapped, and a card left to the
           stream alone keeps a dead Allow / Deny pair under a line promising a
           return that never comes. That is exactly the ALREADY_ANSWERED case
           this branch exists for, and the stream-blocked state as well — the
           board names the latter "Not tracking the store", and it is the state
           the e2e case constructs. Reloading first would swallow the outcome
           into the reload, which the rule above forbids; staying put would leave
           the promise unkept. Both halves are the point, so neither is dropped. */
        showCloseNote(row, failure.message, true);
        console.error('attention board: answer failed - HTTP ' + response.status, body);
        window.setTimeout(function () { window.location.reload(); }, 2500);
        return;
      }
      /* Every other failure keeps the raw body, matching the form path: the body
         is what a reader needs to tell a code fault from a connection problem,
         so a genuinely malformed request still reads as malformed rather than
         being swallowed into a friendly line. */
      showCloseNote(row, 'Answer failed - HTTP ' + response.status + ' - ' + body, true);
    });
  }).catch(function (error) {
    button.disabled = false;
    showCloseNote(row, 'Could not reach the store: ' + error, true);
  });
});
/* kindAll records one answer of the given kind on every tab. Dismiss uses it
   with "skip" and the corner X with "hidden": both are whole-card acts, so a
   multi-question item records the kind on every question rather than on none.
   The two kinds stay distinct — see the X's dispatch comment — so this shares
   the shape, never the meaning. */
function kindAll(form, kind) {
  var entries = [];
  form.querySelectorAll('.panel').forEach(function (panel) {
    entries.push({ question: panel.getAttribute('data-question') || '', kind: kind });
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
      if (failure.staleCard) {
        /* The card went stale between this page being drawn and this answer
           arriving — either the item left the queue, or another arm answered it
           first. Neither is a malformed request, and in both the operator's click
           carried an outcome the raw body buries. The line is shown AND the page
           returns to the queue, in that order and with a beat between them.
           Reloading first would swallow the outcome into a reload, which the rule
           above forbids; staying put would leave a card offering a control that
           cannot succeed — for an item that no longer exists, or one the board
           already holds as a dimmed record. Both halves are the point, so neither
           is dropped. */
        showNote(form, failure.message, true);
        console.error('attention board: answer failed - HTTP ' + response.status, body);
        window.setTimeout(function () { window.location.reload(); }, 2500);
        return;
      }
      showNote(form, 'Answer failed - HTTP ' + response.status + ' - ' + body, true);
      console.error('attention board: answer failed - HTTP ' + response.status, body);
    });
  }).catch(function (error) {
    showNote(form, 'Answer failed - ' + String(error), true);
    console.error('attention board: answer failed - ' + String(error));
  });
}
/* answerFailure reads the store's error envelope and reports whether the card in
   front of the operator has gone STALE — the item moved underneath it since the
   page drew it. Two codes say so, and they say different things:

     ITEM_CLOSED       the item left the queue: nobody answered, nothing routed.
     ALREADY_ANSWERED  another arm answered it first. The click DID land — this
                       is a lost race, not a malformed request — so the card is a
                       stale copy of an item the board already renders as a
                       dimmed record.

   Both leave a card offering a control that cannot succeed, which is the defect
   this branch exists to fix: the operator's own report was of a second click on
   an already-answered card, and the raw ALREADY_ANSWERED body it printed.

   Every other failure keeps the raw body, because the body is what a reader needs
   to tell a code fault from a connection problem — so a genuinely malformed
   request still reads as malformed rather than being swallowed into a friendly
   line. A body that is not JSON at all — a proxy's error page, a truncated
   response — reads as "not this case" rather than throwing, so the caller still
   reaches its raw-body line. */
function answerFailure(body) {
  var envelope;
  try { envelope = JSON.parse(body); } catch (error) { return { staleCard: false }; }
  var failure = envelope && envelope.error;
  if (!failure) { return { staleCard: false }; }
  if (failure.code === 'ITEM_CLOSED') {
    var closedAt = (failure.details && failure.details.closed_at) || '';
    return {
      staleCard: true,
      message: closedAt
        ? 'This item was already closed at ' + closedAt + ' - it left the queue before this answer arrived. Returning to the queue.'
        : 'This item had already left the queue before this answer arrived. Returning to the queue.'
    };
  }
  if (failure.code === 'ALREADY_ANSWERED') {
    /* The line names what happened and the return to the queue replaces the stale
       form with the record of the answer that actually landed — which is the card
       the operator asked to keep, so this delivers the dim-and-stay decision to a
       client that had gone stale rather than reversing it. */
    return {
      staleCard: true,
      message: 'This item was already answered. Returning to the queue to show what was recorded.'
    };
  }
  return { staleCard: false };
}
function showNote(form, message, isError) {
  var previous = form.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('div');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  form.appendChild(note);
  rememberFailure(form, 'form', message, isError);
}
/* The read-aloud control is a TOGGLE: a click while its own item is playing
   stops that playback, and a click when nothing of its own is playing starts a
   reading. It is type="button" so it never submits the answer form.
   Both the item id and the note's home are found from the ROW, never from a
   form ancestor: the control sits in the card's top-right corner, outside the
   answer form, so a form-ancestor lookup here returns null and this handler
   throws before it ever fetches. The corner X reads the row the same way, for
   the same reason.

   ⚠️ Delegated on the document, NOT bound per button — for the same reason the
   Jump control is, and the reason is a defect rather than a preference. The
   stream replaces a row's whole outerHTML on every event (upsertRow), and a
   listener attached to the old node dies with it. A per-button binding
   therefore leaves every re-rendered card's read-aloud control inert while
   still rendering it correctly: the button is there, the click does nothing,
   no request is made, and no note appears. That is the failure this repo
   already shipped once at v0.19.0, and the Jump control was converted to
   delegation to fix its half of it — the read-aloud control was left behind
   until 2026-09-27, when the permission-card ruling made the inert case
   routine rather than occasional.

   ⚠️ What this page is playing lives HERE, in the script's own scope, and NOT
   on the button. upsertRow replaces a row's whole outerHTML, so a message id
   written onto the control dies with the node it was written to — the same
   defect one layer down, and the reason the repaint is re-applied from the map
   rather than trusted to survive. Two consequences worth stating:

   The click handler decides from the MAP, never from the button's data-state,
   so what the control DOES is right even in the window before the repaint
   lands. And the map is declared in this outer scope rather than inside the
   stream's IIFE because both halves need it: the handler below reads it, and
   upsertRow re-applies it after a swap. It does not survive a full page
   reload — that limit, and why it was accepted, is recorded in the task's
   # Results. */
var speaking = {};
/* The read-aloud control for an item, found by comparing data-item-id rather
   than by building a selector — the same reason the stream's own findRow
   gives: a generated id is not the place to rely on. It is a second lookup
   rather than a call to findRow because findRow lives inside the stream's IIFE
   below, and this scope cannot reach it. */
function speakControl(itemID) {
  var rows = document.querySelectorAll('li.item');
  for (var i = 0; i < rows.length; i++) {
    if (rows[i].getAttribute('data-item-id') === itemID) {
      return rows[i].querySelector('button[data-speak]');
    }
  }
  return null;
}
function speakButtonState(button, isSpeaking) {
  if (!button) { return; }
  if (isSpeaking) {
    button.setAttribute('data-state', 'speaking');
    button.setAttribute('aria-label', 'Stop reading');
    button.setAttribute('title', 'Stop reading');
    return;
  }
  button.removeAttribute('data-state');
  button.setAttribute('aria-label', 'Read aloud');
  button.setAttribute('title', 'Read aloud');
}
function startReading(itemID, noteHost) {
  fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/speak', { method: 'POST' })
    .then(function (response) {
      return response.text().then(function (body) {
        if (!response.ok) {
          showNote(noteHost, 'Read aloud failed - HTTP ' + response.status + ' - ' + body, true);
          console.error('attention board: read aloud failed - HTTP ' + response.status, body);
          return;
        }
        /* The id is what makes the next click a stop, so a response that
           carries none leaves the control a start button rather than marking
           it as playing and stranding the operator. */
        var messageID = '';
        try { messageID = (JSON.parse(body) || {}).message_id || ''; } catch (error) { messageID = ''; }
        if (messageID) {
          speaking[itemID] = messageID;
          speakButtonState(speakControl(itemID), true);
        }
        showNote(noteHost, 'Reading aloud (' + body + ')', false);
      });
    }).catch(function (error) {
      showNote(noteHost, 'Read aloud failed - ' + String(error), true);
      console.error('attention board: read aloud failed - ' + String(error));
    });
}
function stopReading(itemID, messageID, noteHost) {
  fetch('/api/1.0/attention/' + encodeURIComponent(itemID) + '/cancel', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ message_id: messageID })
  })
    .then(function (response) {
      return response.text().then(function (body) {
        /* Cleared on a 404 as well as on success: that status means the id
           aged out of the tts server's status table, so there is nothing left
           to stop and the reading is over either way. A control left showing
           "speaking" with nothing playing is the one-way control this toggle
           replaces. Only a genuine failure keeps the state, so a retry is
           still possible. */
        if (response.ok || response.status === 404) {
          delete speaking[itemID];
          speakButtonState(speakControl(itemID), false);
        }
        if (response.ok) {
          showNote(noteHost, 'Stopped reading', false);
          return;
        }
        if (response.status === 404) {
          showNote(noteHost, 'Stopped reading - it had already finished', false);
          return;
        }
        showNote(noteHost, 'Stop reading failed - HTTP ' + response.status + ' - ' + body, true);
      });
    }).catch(function (error) {
      showNote(noteHost, 'Stop reading failed - ' + String(error), true);
    });
}
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-speak]');
  if (!button) { return; }
  var row = button.closest('li.item');
  var itemID = row.getAttribute('data-item-id');
  var noteHost = row.querySelector('form.answer') || row;
  var messageID = speaking[itemID];
  if (messageID) {
    stopReading(itemID, messageID, noteHost);
    return;
  }
  startReading(itemID, noteHost);
});
/* ⚠️ The Jump control is a button, not a link, and the difference is the
   operator's ask: "so we dont switch the screen". A link navigates the browser
   to the fleet-jump server, taking the board out of view. This calls the
   board's own endpoint, which performs the jump server-side and answers 204 —
   so a click switches WezTerm and leaves this page exactly where it was. */
/* ⚠️ Delegated on the document, NOT bound per button — and the difference is a
   defect, not a preference. The stream below replaces a row's whole outerHTML
   on every event (upsertRow), and a listener attached to the old node dies
   with it. A per-button binding therefore leaves every re-rendered card's
   control inert while still rendering it correctly — the button is there, the
   click does nothing, no request is made, and no note appears. That is the
   same failure this repo already shipped once: v0.19.0's answer handler looked
   up a node that was not there, threw, and fired no request at all while the
   control still looked right.
   The document rather than ul.items because ensureList() creates that
   container lazily — a listener bound to it at load would miss the first row,
   and binding it inside ensureList would rebind on every empty->non-empty
   transition. The document covers both cases once. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-jump]');
  if (!button) { return; }
  var row = button.closest('li.item');
  fetch(button.getAttribute('data-jump'), { method: 'GET' })
    .then(function (response) {
      if (response.ok) { showJumpNote(row, 'Jumped.', false); return; }
      return response.text().then(function (body) {
        showJumpNote(row, 'Jump failed - HTTP ' + response.status + ' - ' + body, true);
        console.error('attention board: jump failed - HTTP ' + response.status, body);
      });
    }).catch(function (error) {
      showJumpNote(row, 'Jump failed - ' + String(error), true);
      console.error('attention board: jump failed - ' + String(error));
    });
});
/* The card's machine identity, behind an "i" at the card's top right. The
   panel ships hidden in the served markup and this only flips that state, so a
   row the stream re-renders comes back closed with the control still
   operable — never open-by-default, and never rendered-and-dead.

   ⚠️ Delegated on the document, NOT bound per button — for the same reason the
   Jump and read-aloud controls are, and the reason is a defect rather than a
   preference. The stream replaces a row's whole outerHTML on every event
   (upsertRow), and a listener attached to the old node dies with it: the
   button is there, the click does nothing, and no panel opens. That is the
   failure this repo already shipped once at v0.19.0.

   ⚠️ The state is read from the button's own aria-expanded attribute rather
   than from a page-local map, and that is deliberate rather than incidental:
   the attribute is server-rendered on every row the stream sends, so it is the
   one place the open/closed state survives a swap without a second store to
   keep in step. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-info-toggle]');
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  var panel = row.querySelector('[data-info-panel]');
  if (!panel) { return; }
  if (button.getAttribute('aria-expanded') === 'true') {
    button.setAttribute('aria-expanded', 'false');
    panel.setAttribute('hidden', '');
    return;
  }
  button.setAttribute('aria-expanded', 'true');
  panel.removeAttribute('hidden');
});
function showJumpNote(row, message, isError) {
  var container = row.querySelector('.jump');
  var previous = container.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('span');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  container.appendChild(note);
  rememberFailure(row, 'jump', message, isError);
}
/* ⚠️ The acknowledge path closes the item — open -> closed — and on a
   report-only card it is the only move that card offers. It posts the board as
   the arm that caused the close, which is what lets a reader tell a board
   acknowledgement from a producer withdrawing its own item or from the store's
   producer-exit sweep: neither of those records an answered_by at all.
   answered_at stays unset, because an ack item routes nothing back to its
   producer.
   It is a named function rather than an inline listener because two controls
   reach it — the acknowledge button and the corner X — and both must perform
   the same write, not two writes that happen to agree.
   ⚠️ RENAMED 2026-09-27 from closeAck. It serves a permission card's X as
   well as an ack card's Acknowledge, so a name claiming one mechanism would
   misdescribe the other — the same reason the note's verb is read off the row
   rather than hardcoded. */
function closeCard(row) {
  /* The verb is per-mechanism: an ack card is acknowledged, a permission card
     is cleared. The note is the only feedback this handler gives on a row whose
     removal comes from the stream rather than from here. */
  var acknowledged = !!row.querySelector('[data-ack]');
  fetch('/api/1.0/attention/' + encodeURIComponent(row.getAttribute('data-item-id')) + '/close', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    /* automation is the page's own navigator.webdriver reading — the one
       answered-client member the body may carry; the store reads user_agent
       and remote_addr from the request itself. */
    body: JSON.stringify({ answered_by: 'attention-board', automation: navigator.webdriver })
  }).then(function (response) {
    if (response.ok) { showCloseNote(row, acknowledged ? 'Acknowledged.' : 'Cleared.', false); return; }
    return response.text().then(function (body) {
      showCloseNote(row, (acknowledged ? 'Acknowledge' : 'Clear') + ' failed - HTTP ' + response.status + ' - ' + body, true);
      /* The action word is fixed at "close" rather than read off the row the way
         the note's verb is. The note addresses the operator and names the
         control they pressed; a console line is grepped, so a stable word is
         what makes it findable. The two surfaces are allowed to differ, and the
         note is the one that must not change. */
      console.error('attention board: close failed - HTTP ' + response.status, body);
    });
  }).catch(function (error) {
    showCloseNote(row, (acknowledged ? 'Acknowledge' : 'Clear') + ' failed - ' + String(error), true);
    console.error('attention board: close failed - ' + String(error));
  });
}
/* ⚠️ Delegated on the document for the tab strip's reason: a re-rendered row's
   node takes its per-button listener with it, so Acknowledge stops acting. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-ack]');
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  closeCard(row);
});
/* The corner X is one affordance whose act is the mechanism's own dominant act,
   which is why this handler dispatches rather than posting: on a message card
   it is the Dismiss the card already carries (the skip answer, open -> answered,
   answer.kind: skip), on an ack card it is the Acknowledge it already carries
   (the close, open -> closed, answered_at unset), and on a permission card it is
   the Deny control the card carries (the answer, open -> answered, decision:
   deny). The class split that stood here until 2026-10-16 is gone: the pair
   renders on every permission card, so the X answers on every one of them. It
   adds no transition and no field — see [[Attention Item Schema]]
   § Answer routing, § The corner X.
   ⚠️ THE PERMISSION BRANCH NO LONGER SPLITS BY CLASS, from 2026-10-16. It once
   did: the X answered where the Deny control rendered and CLEARED where it did
   not, and that split is gone because the pair now renders on every permission
   card. The reading that defended the clearing arm is kept, because it is the
   evidence the operator's ruling rests on: the schema forbids an arm ANSWERING a
   permission item, and clearing was defended as "not deciding". The second half
   of that reading was false. A clear on a permission item is not a lesser act
   than an answer — it is a fatal one. closed is terminal, the store's transition
   table carries no closed -> answered row, so the parked worker's own arm answer
   is refused with ITEM_CLOSED and the gate never releases: the worker sits its
   full timeout to the auto-deny. A clear does not preserve the operator's
   decision, it consumes the only path that decision had. So the X now ANSWERS on
   every permission card, dispatching the Deny button exactly as the message
   branch dispatches Dismiss.
   There is deliberately no third branch below: a permission row carries no
   form.answer, so a row with no Deny control falls through to closeCard exactly
   as an ack row does. The mechanisms share one dispatch because they share one
   act per class, and it is delegated on the document for the tab strip's reason.
   ⚠️ A dimmed record card now carries no data-corner-x button at all, so this
   listener never fires on one — which is why the fall-through to closeCard on a
   dimmed row can no longer happen. */
document.addEventListener('click', function (event) {
  if (!event.target || !event.target.closest) { return; }
  var button = event.target.closest('button[data-corner-x]');
  if (!button) { return; }
  var row = button.closest('li.item');
  if (!row) { return; }
  var form = row.querySelector('form.answer');
  /* A message card's X is the "hidden" submit, dispatched rather than
     re-implemented: the form's own submit handler owns that payload, the
     ITEM_CLOSED branch and the note placement, and a second copy here would
     be a second thing to keep in step.
     ⚠️ It is NOT the Dismiss submit any more, and the two are deliberately
     different acts: Dismiss writes "skip" and the watcher delivers it to the
     asking session as a decline, while this writes "hidden" and delivers
     nothing — the operator answered in the pane and only wanted the card off
     the board. Routing the X back to Dismiss re-creates exactly the defect it
     was separated to fix. The button carries the hidden attribute so it
     renders nothing; it exists only as this dispatch's target. */
  if (form) {
    var hide = form.querySelector('button[value=hidden]');
    if (hide) { hide.click(); return; }
  }
  /* A permission card's X is the Deny control, dispatched for the message
     branch's reason: the data-decision handler owns the payload, the stale-card
     classification and the reload, and a second copy here would be a second
     thing to keep in step. Its PRESENCE is the class test — the control renders
     under 'and .Decide (not .Dimmed)', and .Decide is true for every permission
     row, so finding it means this is a permission row and the X answers. The
     closeCard fall-through below is therefore unreachable for a permission row;
     it remains the ack path and the message-without-form path. */
  var deny = row.querySelector('button[data-decision=deny]');
  if (deny) { deny.click(); return; }
  closeCard(row);
});
/* ⚠️ The container is OPTIONAL, and that is not defensive coding — it is the
   defect this function shipped with until 2026-09-27. A permission card
   carried no actions container then (only message and ack cards did), so
   row.querySelector returned
   null and the next call threw a TypeError on EVERY X-click on a permission
   card. The close itself succeeded and the row still left the page, because the
   stream removes it rather than this handler — which is exactly why the throw
   was invisible: the operator saw the right outcome and only the console took
   the error. Falling back to the row keeps the note on a card that has no
   actions row to hold it. */
function showCloseNote(row, message, isError) {
  var container = row.querySelector('.actions') || row;
  var previous = container.querySelector('.note');
  if (previous) { previous.remove(); }
  var note = document.createElement('span');
  note.className = isError ? 'note failed' : 'note';
  note.textContent = message;
  container.appendChild(note);
  rememberFailure(row, 'close', message, isError);
}
/* The most recent failure shown for an item, keyed the way speaking is and
   for the same reason: a note is a child of the row it was rendered into, so
   upsertRow's outerHTML swap destroys it and the operator loses the line they
   were reading mid-sentence. Held here and re-rendered after the swap, because
   the map lives in this page: the server keeps no record of what this page was
   shown.

   ⚠️ A FAILURE only. A success note is not persisted, and each helper clears
   the entry for its item when it renders one: a stale failure note that
   outlives its cause is a worse defect than the one this fixes, because it
   reports a failure that is no longer true. */
var failures = {};
/* rememberFailure records what a note helper just rendered. The row is read
   from the node the note landed in rather than passed in, so the three helpers
   share one recorder: showNote's host is a form or a row, the other two pass
   the row itself, and closest('li.item') is the same row for all of them. */
function rememberFailure(host, kind, message, isError) {
  var row = host.closest('li.item');
  if (!row) { return; }
  var itemID = row.getAttribute('data-item-id');
  if (!itemID) { return; }
  if (!isError) { delete failures[itemID]; return; }
  failures[itemID] = { kind: kind, message: message };
}
/* replayFailure re-renders the failure last shown for a row, through the SAME
   helper that rendered it — so the replayed note's text, placement and class
   are the helper's own and cannot drift from them, which a second renderer
   here could. The kind picks the helper, and the host is resolved exactly as the
   original call site resolved it: showNote's host is the row's answer form
   where the row carries one and the row itself where it does not, which is the
   same reading the read-aloud handler makes. */
function replayFailure(row) {
  if (!row) { return; }
  var failure = failures[row.getAttribute('data-item-id')];
  if (!failure) { return; }
  if (failure.kind === 'jump') { showJumpNote(row, failure.message, true); return; }
  if (failure.kind === 'close') { showCloseNote(row, failure.message, true); return; }
  showNote(row.querySelector('form.answer') || row, failure.message, true);
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
  /* The board's own view filter. It hides two kinds of row — the dimmed record
     cards (answered items the board keeps as evidence) and flagged cards — so
     the page answers "what is left" rather than showing records or parked
     misroutes beside open prompts. Two properties are load bearing and neither
     is cosmetic.

     A filtered row is REMOVED, not hidden. A display:none rule leaves the node
     in the document, so a selector, a script or the operator's own assistive
     tooling still finds a card the board claims not to be showing.

     And the filter runs on the stream path too, not only at first paint. The
     channel sends a rendered row and this page swaps it in, so a filter that
     ran once at load would leak every row that arrived afterwards — including
     a card that becomes answered or flagged while the board is open, which is
     exactly the row a filter exists to hide. */
  var button = document.querySelector('button[data-board-filter]');
  var flagButton = document.querySelector('button[data-flag-filter]');
  /* The board's stream health, the other control in this row. It ships hidden
     and only the stream's own handlers move it, so nothing about a board whose
     stream is healthy changes. */
  var stale = document.querySelector('[data-stream-stale]');
  /* Mirrors boardHideParam / boardFlagsParam and their values in Go — the one
     thing in this file that exists in two languages. */
  var HIDE_PARAM = 'hide';
  var HIDE_NONE = 'none';
  var FLAGS_PARAM = 'flags';
  var FLAGS_FLAGGED = 'flagged';
  var FLAGS_NONE = 'none';
  /* Both defaults are ON: only an explicit off-spelling turns a filter off, and
     absence (or any unrecognised value) leaves it on. Read as "not none"
     rather than "is answered" so an unrecognised value falls back to the
     default instead of silently disabling the filter.
     ⚠️ The two are read INDEPENDENTLY. Each parameter governs its own set, so
     hide=none un-hides the answered records and leaves the flag filter exactly
     where it was — it is not a union view, and reading it as one silently
     dropped the flood control when the operator only asked to see answers. */
  var params = new URLSearchParams(window.location.search);
  var hideOn = params.get(HIDE_PARAM) !== HIDE_NONE;
  var flagsOn = params.get(FLAGS_PARAM) !== FLAGS_NONE;
  /* The rows the filters are hiding, in board order. Each entry carries the id
     of the row that followed it, so a restore returns it to its own place
     rather than to the foot of the board. It holds rendered HTML rather than
     a detached node: a parked row is either a dimmed record or a flagged open
     card whose typed state the operator is not looking at, and the markup is
     the server's own renderer output either way. Each entry also carries the
     two marks the predicate reads, so a row can be re-evaluated without
     reparsing its HTML. */
  var parked = [];

  function findRow(itemID) {
    /* Compared rather than selected: the id lands in a selector string
       otherwise, and a generated id is not the place to rely on. */
    var rows = document.querySelectorAll('li.item');
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].getAttribute('data-item-id') === itemID) { return rows[i]; }
    }
    return null;
  }
  /* The list, created in place of the empty-state paragraph when the board
     rendered one. The stream needed this already; the filter needs it too,
     because a board the filter emptied is not an empty board — its rows are
     parked, not gone. */
  function ensureList() {
    var list = document.querySelector('ul.items');
    if (list) { return list; }
    var empty = document.querySelector('p.empty');
    if (!empty) { return null; }
    list = document.createElement('ul');
    list.className = 'items';
    empty.parentNode.replaceChild(list, empty);
    return list;
  }
  function nextRowID(row) {
    var next = row.nextElementSibling;
    while (next && !next.classList.contains('item')) { next = next.nextElementSibling; }
    return next ? next.getAttribute('data-item-id') : null;
  }
  function isAnswered(row) { return row.classList.contains('dimmed'); }
  function isFlagged(row) { return row.getAttribute('data-flagged') === 'true'; }
  /* The contract's predicate, verbatim:
       visible(row) = (hideOff || !row.answered) && (flagsOff || !row.flagged)
       hideOff      = (hide == "none")
       flagsOff     = (flags == "none")
     Each half reads only its own switch: a row is hidden by the filter that
     names it, never by the other one's position. */
  function visible(answered, flagged) {
    var hideOff = !hideOn;
    var flagsOff = !flagsOn;
    return (hideOff || !answered) && (flagsOff || !flagged);
  }
  function park(row) {
    var id = row.getAttribute('data-item-id');
    /* Re-parking replaces rather than appends: a row that streams an update
       while hidden arrives here a second time, and two entries for one id
       would restore it twice. */
    parked = parked.filter(function (entry) { return entry.id !== id; });
    /* Self-carry first: outerHTML serialises attributes, so without it a row
       restored later would come back with the picks of its LAST swap rather
       than the ones on screen when it was parked. */
    carryFormState(row, row);
    parked.push({
      id: id,
      html: row.outerHTML,
      next: nextRowID(row),
      answered: isAnswered(row),
      flagged: isFlagged(row)
    });
    row.remove();
  }
  function unparkEntry(entry) {
    var list = ensureList();
    if (!list) { return; }
    var anchor = entry.next ? findRow(entry.next) : null;
    if (anchor) { anchor.insertAdjacentHTML('beforebegin', entry.html); return; }
    list.insertAdjacentHTML('beforeend', entry.html);
  }
  /* Re-evaluated on EVERY change and PER ROW. Restoring is never a global
     unpark: a row hidden by one filter stays hidden while the other is still
     on, which is the flood flags=none exists to prevent. */
  function applyFilter() {
    var keep = [];
    for (var i = parked.length - 1; i >= 0; i--) {
      var entry = parked[i];
      if (visible(entry.answered, entry.flagged)) { unparkEntry(entry); } else { keep.unshift(entry); }
    }
    parked = keep;
    var rows = document.querySelectorAll('li.item');
    for (var j = 0; j < rows.length; j++) {
      if (!visible(isAnswered(rows[j]), isFlagged(rows[j]))) { park(rows[j]); }
    }
    showEmptyStatement();
  }
  /* Mirrors boardProducerKindParam / boardProducerKindAll in Go. The server
     already renders only the matching rows, so this exists for the STREAM
     path: a frame carries a rendered row for whatever was just pushed, and a
     card of another kind would otherwise appear on a filtered board — the same
     leak the answered filter had to close on this same path.
     ⚠️ A non-matching row is REMOVED rather than parked: an item's producer
     kind is immutable, so nothing can ever bring it back, and the parked list
     is the answered filter's restore buffer rather than a second one. */
  var KIND_PARAM = 'producer_kind';
  var KIND_ALL = 'all';
  var KIND_DEFAULT = 'manager';
  /* The empty state's kind-aware copy. The server renders the same sentence,
     but both collapseIfEmpty() and showEmptyStatement() rebuild the paragraph
     from scratch when the list empties, so a client-side "Nothing needs
     attention." would put the lie back on a board whose switch names the hidden
     cards — the production state, since no producer emits manager. */
  /* ⚠️ No em dash: sibling specs use "—" as a sentinel for "a placeholder was
     rendered where an unresolvable value belongs", and prose punctuation would
     trip that guard on a correct page. */
  var KIND_EMPTY_COPY = 'No cards of this kind need attention. The others are hidden by the kind filter above.';
  var kind = new URLSearchParams(window.location.search).get(KIND_PARAM) || KIND_DEFAULT;
  /* The count beside the switch, kept true after load. Both populations arrive
     on the kind link, because the rows the server withheld were never sent — the
     document cannot count them, and only the server knows how many of them are
     answered. What the script adds is the rows the STREAM delivers: applyKindFilter
     removes them, so the board ends up withholding more than the load-time count
     names. The span itself is rendered only when there is something to say, so
     this both updates it and creates or removes it.
     ⚠️ Read from the link rather than captured into variables at parse time: the
     link is resolved further down this script, and a captured value would be
     read before that assignment. The attributes never change after load.
     ⚠️ Streamed rows are keyed by item id rather than tallied: a card's update
     arrives as a second frame, and an id-keyed map makes the recount idempotent
     where a counter would inflate. The value is the row's dimmed state, which
     decides whether it counts at all — and which changes when the card is
     answered. */
  function kindHiddenBase(attribute) {
    if (!kindLink) { return 0; }
    return parseInt(kindLink.getAttribute(attribute), 10) || 0;
  }
  var kindStreamed = {};
  function updateKindCount() {
    var total = 0;
    if (kind !== KIND_ALL) {
      /* hideOn rather than a load-time constant: syncFromURL re-reads it on
         every URL write, so the count follows the answered toggle instead of
         describing the view the page was served with. */
      total = hideOn ? kindHiddenBase('data-hidden-open') : kindHiddenBase('data-hidden-all');
      for (var id in kindStreamed) {
        /* The same rule the server applies: an answered row is already withheld
           by the answered filter, so the kind filter is not what hides it. */
        if (!kindStreamed[id] || !hideOn) { total++; }
      }
    }
    if (total === 0) {
      var stale = document.querySelector('[data-kind-hidden]');
      if (stale) { stale.remove(); }
      return;
    }
    var el = document.querySelector('[data-kind-hidden]');
    if (!el) {
      if (!kindLink) { return; }
      /* Built rather than inserted as markup: the rendered span and a markup
         string would be the same text, and the Go suite asserts on the served
         markup to prove the all-kinds view renders no count. */
      el = document.createElement('span');
      el.className = 'kind-hidden';
      el.setAttribute('data-kind-hidden', '');
      kindLink.parentNode.insertBefore(el, kindLink.nextSibling);
    }
    el.textContent = total + ' hidden';
  }
  function applyKindFilter() {
    if (kind !== KIND_ALL) {
      var rows = document.querySelectorAll('li.item');
      for (var i = 0; i < rows.length; i++) {
        if (rows[i].getAttribute('data-producer-kind') === kind) { continue; }
        /* Mirrors filterByProducerKind: a card no live manager can relay is
           shown whatever its kind, so a streamed one must survive this pass. */
        if (rows[i].getAttribute('data-uncovered') === 'true') { continue; }
        /* Recorded before the row leaves the document: whether it counts depends
           on whether it was dimmed, which only the node knows. */
        kindStreamed[rows[i].getAttribute('data-item-id')] =
          rows[i].classList.contains('dimmed');
        rows[i].remove();
      }
    }
    updateKindCount();
  }
  /* ⚠️ The kind link's href is SERVER-RENDERED from the request, while the
     answered toggle below rewrites the query with history.replaceState. Left
     alone the link keeps its load-time query, so following it after a toggle
     silently resets the other filter: toggling Hide answered OFF (URL gains
     hide=none) and then clicking the kind link would navigate to the stale
     query, drop hide=none and re-hide the answered records — precisely the
     mutual reset the server-side KindHref comment claims is prevented. The
     Go test could not see it because it requests ?hide=none directly rather
     than toggling.
     Recomputed after every URL write; the server-rendered href stays as the
     no-JS fallback, which is the only case where it can still be right. */
  var kindLink = document.querySelector('[data-board-kind]');
  function syncKindHref() {
    if (!kindLink) { return; }
    var target = kindLink.getAttribute('data-all-kinds') === 'true' ? KIND_DEFAULT : KIND_ALL;
    var next = new URL(window.location.href);
    next.searchParams.set(KIND_PARAM, target);
    kindLink.setAttribute('href', next.pathname + next.search);
  }
  function forget(itemID) {
    parked = parked.filter(function (entry) { return entry.id !== itemID; });
  }
  function syncFromURL(url) {
    hideOn = url.searchParams.get(HIDE_PARAM) !== HIDE_NONE;
    flagsOn = url.searchParams.get(FLAGS_PARAM) !== FLAGS_NONE;
  }
  function renderSwitchState() {
    if (button) { button.setAttribute('aria-checked', hideOn ? 'true' : 'false'); }
    if (flagButton) { flagButton.setAttribute('aria-checked', flagsOn ? 'true' : 'false'); }
  }
  /* replaceState, not pushState. The filters are a view, and a view that
     filled the Back stack would make Back mean "undo my filter" on some
     presses and "leave the page" on others — and with no popstate handler a
     pushState entry would also leave the URL and the page disagreeing after a
     Back. This changes the URL without a navigation, which is what keeps the
     no-reload property intact. */
  if (button) {
    button.addEventListener('click', function () {
      var url = new URL(window.location.href);
      if (url.searchParams.get(HIDE_PARAM) === HIDE_NONE) {
        /* Turning the answered filter back on: absence is the default spelling. */
        url.searchParams.delete(HIDE_PARAM);
      } else {
        /* Only the answered parameter moves. The flag parameter is left exactly
           as it was: this switch governs answered records, and writing flags
           from here is what made one switch move the other. */
        url.searchParams.set(HIDE_PARAM, HIDE_NONE);
      }
      window.history.replaceState(null, '', url);
      syncFromURL(url);
      renderSwitchState();
      /* The kind link carries the query it was rendered from, so it has to be
         rebuilt from the URL that just changed — see syncKindHref. */
      syncKindHref();
      applyFilter();
      /* The answered toggle moves which of the two counts applies: with the
         filter ON the answered records are withheld anyway, so the kind filter
         is not what hides them. */
      updateKindCount();
    });
  }
  if (flagButton) {
    flagButton.addEventListener('click', function () {
      var url = new URL(window.location.href);
      /* Read from this switch's own parameter alone: the answered filter's
         position says nothing about whether flagged cards are being hidden. */
      var flagsOff = url.searchParams.get(FLAGS_PARAM) === FLAGS_NONE;
      if (flagsOff) {
        /* Turning the flag filter ON. The answered filter is left where the
           operator put it — the two switches are independent, so this one
           never writes hide. */
        url.searchParams.set(FLAGS_PARAM, FLAGS_FLAGGED);
      } else {
        url.searchParams.set(FLAGS_PARAM, FLAGS_NONE);
      }
      window.history.replaceState(null, '', url);
      syncFromURL(url);
      renderSwitchState();
      /* The kind link carries the query it was rendered from, so it has to be
         rebuilt from the URL that just changed — see syncKindHref. */
      syncKindHref();
      applyFilter();
      /* No updateKindCount here: the kind count is a function of the answered
         filter's position and the streamed rows, and this click moves neither
         now that the two switches are independent. */
    });
  }
  /* Applied at load as well as on click, so a URL that arrived carrying either
     filter renders the filtered view instead of flashing the full one, and so
     a page that starts unfiltered still establishes the predicate for rows
     that arrive later. The script runs while the parser is still inside the
     body, so this lands before the first paint rather than as a correction to
     it. */
  applyFilter();
  syncKindHref();
  updateKindCount();
  /* The toggle writes the URL with replaceState, so it rebuilds the link
     itself; this covers every OTHER way the URL can change under the page — a
     Back/Forward restore out of bfcache, or any future in-page writer that
     forgets to call syncKindHref. Cheap, and the failure it prevents is silent. */
  window.addEventListener('popstate', syncKindHref);
  function collapseIfEmpty() {
    var list = document.querySelector('ul.items');
    if (!list || list.querySelector('li.item')) { return; }
    /* A board the filter emptied is not an empty board: the store still holds
       the parked records, and collapsing here would take away the list they
       restore into. */
    if (parked.length > 0) { return; }
    var empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = (kind === KIND_ALL) ? 'Nothing needs attention.' : KIND_EMPTY_COPY;
    list.parentNode.replaceChild(empty, list);
  }
  /* Renders the board's empty-state paragraph in place of an emptied list
     even while rows are parked. collapseIfEmpty() keeps its parked guard — a
     board the filter emptied is not an empty board — so it cannot reach this
     state; this function does, without touching parked: unpark() rebuilds the
     list through ensureList(), so the toggle still restores every parked row. */
  function showEmptyStatement() {
    var list = document.querySelector('ul.items');
    if (!list || list.querySelector('li.item')) { return; }
    if (document.querySelector('p.empty')) { return; }
    var empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = (kind === KIND_ALL) ? 'Nothing needs attention.' : KIND_EMPTY_COPY;
    list.parentNode.replaceChild(empty, list);
  }
  /* A row's update is PREPARED on a detached node and only then committed.
     ⚠️ Load bearing: the live path used to swap first and repair after, so a
     throw between the two left the row swapped with its state missing — worst
     of all the filter, which left a card that became answered standing OPEN on
     a board whose own switch said it was hidden. Preparing first makes the swap
     the LAST fallible step, and is also how a PARKED row's note is replayed. */
  function preparedHTML(html, itemID) {
    var live = findRow(itemID);
    var carry = !!(live && live.querySelector('form'));
    if (!failures[itemID] && !speaking[itemID] && !carry) { return html; }
    var detached = document.createElement('div');
    detached.innerHTML = html;
    var row = detached.firstElementChild;
    if (!row) { return html; }
    if (failures[itemID]) { replayFailure(row); }
    if (speaking[itemID]) { speakButtonState(speakButtonIn(row), true); }
    if (carry) { carryFormState(live, row); }
    return row.outerHTML;
  }
  /* The operator's unsent input, carried from the live row onto the server's
     rendering before the swap. The server never renders a pick, a typed field
     or a chosen tab — it cannot know them — so every stream event for a row
     (a 👍/👎 echo above all, since the operator clicks it WHILE answering)
     used to replace that row with blank controls and silently discard the
     answer in progress.
     ⚠️ Written as ATTRIBUTES, not properties: the prepared node is serialised
     through outerHTML, which carries the checked and value attributes and drops
     the live properties. A control the new markup no longer carries (the card
     became an answered record) has nothing to land on and is dropped, which is
     right — the answer was given. */
  /* The key carries the question's own TEXT, so a producer re-push that rewords
     a question does not keep a pick the operator made against the old wording:
     the key no longer matches and that panel comes back blank. */
  function controlKey(input) {
    var form = input.closest('form');
    var panel = input.closest('.panel');
    var question = panel ? panel.querySelector('.question') : null;
    var key = [
      form ? form.className : '',
      panel ? panel.getAttribute('data-question') : '',
      question ? question.textContent : '',
      input.type,
      input.name,
      input.className
    ];
    if (input.type === 'radio' || input.type === 'checkbox') {
      key.push(input.hasAttribute('data-other') ? 'data-other' : 'option:' + input.value);
    }
    return JSON.stringify(key);
  }
  function restoreFocus(itemID, key, caret) {
    var row = findRow(itemID);
    if (!row) { return; }
    var inputs = row.querySelectorAll('input');
    for (var i = 0; i < inputs.length; i++) {
      if (controlKey(inputs[i]) !== key) { continue; }
      inputs[i].focus();
      if (caret) { inputs[i].setSelectionRange(caret[0], caret[1]); }
      return;
    }
  }
  function carryFormState(live, row) {
    var targets = {};
    row.querySelectorAll('input').forEach(function (input) {
      var key = controlKey(input);
      if (!targets[key]) { targets[key] = []; }
      targets[key].push(input);
    });
    var seen = {};
    live.querySelectorAll('input').forEach(function (input) {
      var key = controlKey(input);
      var index = seen[key] || 0;
      seen[key] = index + 1;
      var target = targets[key] && targets[key][index];
      if (!target) { return; }
      if (input.type === 'radio' || input.type === 'checkbox') {
        if (input.checked) { target.setAttribute('checked', ''); }
        else { target.removeAttribute('checked'); }
      } else if (input.type === 'text') {
        target.setAttribute('value', input.value);
      }
    });
    var activeTab = live.querySelector('.tabs button[data-tab].active');
    if (!activeTab) { return; }
    var name = activeTab.getAttribute('data-tab');
    var match = false;
    row.querySelectorAll('.tabs button[data-tab]').forEach(function (tab) {
      if (tab.getAttribute('data-tab') === name) { match = true; }
    });
    if (!match) { return; }
    row.querySelectorAll('.tabs button[data-tab]').forEach(function (tab) {
      tab.classList.toggle('active', tab.getAttribute('data-tab') === name);
    });
    row.querySelectorAll('.panel').forEach(function (panel) {
      if (panel.getAttribute('data-question') === name) { panel.removeAttribute('hidden'); }
      else { panel.setAttribute('hidden', ''); }
    });
  }
  /* The read-aloud control within a row not yet in the document — speakControl
     searches the document, which an uncommitted node is not part of. */
  function speakButtonIn(row) {
    return row ? row.querySelector('button[data-speak]') : null;
  }
  function upsertRow(itemID, html) {
    /* Prepared first: a throw in here leaves the board untouched. */
    var prepared = preparedHTML(html, itemID);
    var hidden = -1;
    for (var i = 0; i < parked.length; i++) {
      if (parked[i].id === itemID) { hidden = i; }
    }
    if (hidden >= 0) {
      /* The row is hidden by the filter, so it is not in the document and
         findRow cannot see it. Updating the parked entry in place keeps the
         place it restores to: appending the row to the list and letting
         applyFilter re-park it would read a fresh anchor at the foot of the
         board, which is null, and the record would come back in the wrong
         position — the exact failure the anchor exists to prevent. The row
         stays hidden because a parked row is answered, and an answered item
         never returns to open (§ Lifecycle), so its update is dimmed too. */
      parked[hidden].html = prepared;
      /* The entry's own marks are re-derived from the prepared markup and the
         filter re-run, so a parked card that is unflagged (or answered) while
         the page is open returns to the board instead of staying parked at its
         stale mark. */
      var detached = document.createElement('div');
      detached.innerHTML = prepared;
      var node = detached.firstElementChild;
      if (node) {
        parked[hidden].answered = isAnswered(node);
        parked[hidden].flagged = isFlagged(node);
      }
      applyFilter();
      return;
    }
    var row = findRow(itemID);
    if (row) {
      /* The operator may be typing in this row when an unrelated event lands:
         the text is carried by preparedHTML, and focus and caret are put back
         here, so the next keystroke still reaches the field. */
      var focused = document.activeElement;
      var focusKey = (focused && focused.tagName === 'INPUT' && row.contains(focused)) ? controlKey(focused) : null;
      var caret = (focusKey && focused.type === 'text') ? [focused.selectionStart, focused.selectionEnd] : null;
      row.outerHTML = prepared;
      if (focusKey) { restoreFocus(itemID, focusKey, caret); }
    } else {
      var list = ensureList();
      if (!list) { return; }
      list.insertAdjacentHTML('beforeend', prepared);
    }
    /* The row is complete as committed, so nothing is re-applied to it here.
       The filters still run after: they are board-level views over every row.

       ⚠️ KIND FIRST, and the order is load bearing rather than stylistic.
       applyFilter PARKS a dimmed row — it detaches it and holds its markup in
       the answered filter's restore buffer — so a kind filter running second
       would never see it: the row is already out of the document, and
       unpark() would put a non-manager answered card back on a manager-only
       board when the operator toggles Hide answered off. Running the kind
       filter first removes it while it is still in the document, which is the
       only moment the kind is readable. */
    applyKindFilter();
    applyFilter();
  }
  /* The filter is wired above this guard rather than below it. It is plain DOM
     work, and a browser with no EventSource still renders the board and still
     has to be able to ask what is left. */
  if (typeof EventSource === 'undefined') { return; }
  /* The not-tracking state, shown in the board's control row. It is a state on
     the page rather than a console line because the operator has to be able to
     read it without opening devtools — that is the whole point: a stream that
     has stopped for good (a 404 after a redeploy, a persistent 500) left the
     board frozen at its load-time state with no signal at all, so a stale board
     read as a current one. Tracking is the healthy case, so the callers read
     as what the event means rather than as which way the attribute goes. */
  function setTracking(tracking) {
    if (!stale) { return; }
    stale.hidden = tracking;
  }
  /* Reconnect after a TERMINAL stream failure: EventSource retries a network
     drop itself, but fails the connection for good on a non-200 status or a
     non-event-stream content type — the shape this server returns when the
     baseline render fails — and never asks again, so the state onerror renders
     could never be cleared. See the e2e case "recovers when the stream
     returns a response that is not an event stream". */
  var reconnectDelayMs = 1000;
  var reconnectTimer = null;
  var source = null;
  function connect() {
    source = new EventSource('/api/1.0/attention/stream');
    source.onopen = function () {
      reconnectDelayMs = 1000; /* established, so a later outage backs off from the floor */
    };
    source.onerror = function () {
      setTracking(false);
      /* CONNECTING means EventSource retries by itself; only CLOSED is abandoned. */
      if (source.readyState !== EventSource.CLOSED) { return; }
      if (reconnectTimer !== null) { return; }
      reconnectTimer = window.setTimeout(function () {
        reconnectTimer = null;
        reconnectDelayMs = Math.min(reconnectDelayMs * 2, 30000);
        connect();
      }, reconnectDelayMs);
    };
    source.onmessage = onMessage;
  }
  function onMessage(event) {
    /* Cleared before the frame is read: a message arrived, so the board is
       tracking the store again whatever that frame turns out to say — a frame
       this page cannot parse is logged and skipped below, and skipping it is
       not the same as the stream being down. */
    setTracking(true);
    var change;
    /* A frame the page cannot read is logged and skipped rather than thrown.
       The rest of this handler reads change.type and change.item_id, so a throw
       here aborted the row swap with nothing said; a truncated frame or one from
       a newer store version is not a reason to stop listening, and the stream
       reconnects — itself for a drop, via connect() for the terminal case. */
    try {
      change = JSON.parse(event.data);
    } catch (error) {
      console.error('unreadable stream frame: ' + String(error), event.data);
      return;
    }
    /* ⚠️ The whole application is guarded, not only the parse above it: a throw
       in the swap, the note replay, the repaint, the filter or the remove path
       used to escape as an uncaught error. It carries the item id, because a
       row that failed to update renders like a row that needed none. */
    try {
      if (change.type === 'remove') {
        /* Dropped from the parked set as well as from the board: a row the
           store removed must not come back when the filter is switched off. */
        forget(change.item_id);
        var row = findRow(change.item_id);
        if (row) { row.remove(); }
        collapseIfEmpty();
        showEmptyStatement();
        return;
      }
      upsertRow(change.item_id, change.html);
    } catch (error) {
      /* ⚠️ No 'attention board: ' prefix: that is reserved for the nine ACTION
         lines, and a spec pins its count at nine. This is stream handling, so
         it logs unprefixed exactly as the parse guard above it does.
         ⚠️ The id is read through a guard, not off 'change' directly. The parse
         above succeeds for any JSON document, and the literal 'null' is one:
         'change.type' then throws, this catch runs, and 'change.item_id' would
         throw AGAIN from inside the error path — escaping source.onmessage
         anyway, which is the exact failure this catch exists to close. */
      console.error(
        'could not apply stream frame for ' +
          (change && change.item_id) + ' - ' + String(error),
        change
      );
    }
  }
  connect();
})();
