// cardText.tsx — renders a card body field (payload, question, context, blast
// radius) with markdown links and bare http(s) URLs as anchors, everything else
// as literal text.
//
// This ports pkg/handler/card-text.go, which the retired server-rendered board
// called from its template as `cardtext`. The safety argument is the same: a
// card body is authored by a *session*, so the value reaching an anchor is
// producer input. The scheme allowlist below is therefore closed, and every
// other candidate stays visible as text.
//
// ⚠️ React elements, never dangerouslySetInnerHTML. React escapes text nodes and
// attribute values itself, so the escaping boundary the Go version had to own by
// hand (template.HTML) does not exist here — the only thing this module decides
// is which characters become an anchor.
import type { ReactNode } from "react";

// CARD_LINK_SCHEMES is the closed set of URL schemes a card body may link to. An
// allowlist, not a denylist: it fails closed on a scheme nobody has taught it.
// `obsidian://` is admitted because it is the board's own navigation scheme;
// `javascript:` and `data:` are deliberately absent.
const CARD_LINK_SCHEMES = new Set(["http", "https", "obsidian"]);

// CARD_MARKDOWN_LINK matches `[label](url)`. The label may not contain `]` and
// the url may not contain whitespace or a paren, so a url that itself carries
// parens (`javascript:alert(1)`) does not match — which is the safe direction:
// an unmatched candidate stays literal text.
//
// The `^` is load-bearing for cost: this is tested at every position of the
// body, and an unanchored pattern would scan the rest of the string for a match
// it then rejects. The whitespace class is spelled out as the ASCII set Go's
// `\s` means, so a URL carrying a non-breaking space is cut the same way here as
// there.
const CARD_MARKDOWN_LINK = /^\[([^\]]*)\]\(([^() \t\n\f\r]*)\)/;

// CARD_BARE_URL matches a bare http(s) URL, excluding the characters that end a
// sentence around one — `<`, `>`, quotes and the parens — so a URL wrapped in
// prose is autolinked without swallowing the prose. `)` matters most: without
// it `see (https://example.com)` would link to `https://example.com)`.
const CARD_BARE_URL = /^https?:\/\/[^ \t\n\f\r<>"'()]+/;

// cardScheme returns the scheme of a URL, lowercased, or "" when it carries
// none.
//
// A scheme is the run before the first `:` — but only when that `:` precedes any
// `/`, `?` or `#`, so `example.com/a:b` reports no scheme rather than inventing
// one from a colon inside a path. Case is folded because `JavaScript:` and
// `javascript:` are the same scheme to a browser.
function cardScheme(rawURL: string): string {
  for (let i = 0; i < rawURL.length; i++) {
    const ch = rawURL[i];
    if (ch === ":") return rawURL.slice(0, i).toLowerCase();
    if (ch === "/" || ch === "?" || ch === "#") return "";
  }
  return "";
}

// Segment is one resolved run of the body: an anchor, or literal text.
type Segment =
  | { kind: "link"; href: string; label: string; consumed: number }
  | { kind: "text"; text: string; consumed: number };

// cardLinkAt resolves a markdown link starting at text[i]. null when none starts
// there, which is the common case and costs one character comparison.
//
// The first-character guard keeps the loop linear: the pattern is `^`-anchored,
// so invoking the regexp engine at a position whose character is not `[` is work
// with no possible answer.
function cardLinkAt(text: string, i: number): Segment | null {
  if (text[i] !== "[") return null;
  const match = CARD_MARKDOWN_LINK.exec(text.slice(i));
  if (!match) return null;
  const [whole, label, rawURL] = match;
  if (!CARD_LINK_SCHEMES.has(cardScheme(rawURL))) {
    // Scheme refused: the whole candidate stays visible as text, so the operator
    // can still read what the card tried to do.
    return { kind: "text", text: whole, consumed: whole.length };
  }
  return { kind: "link", href: rawURL, label, consumed: whole.length };
}

// cardBareURLAt resolves a bare http(s) URL starting at text[i].
//
// ⚠️ A URL whose two immediately preceding characters are `](` is emitted as
// text, never as an anchor. That lookbehind stops a payload the producer cut
// mid-link (`[pane 88](http://127.0.0.1:1337/`) from rendering a clickable
// half-link: the URL is real and the link syntax is broken, so linking it would
// point the operator at a 404 while looking correct. It is a fixed two-character
// test, not a parse, so `[label] (url)` still autolinks its URL.
function cardBareURLAt(text: string, i: number): Segment | null {
  if (text[i] !== "h") return null;
  const match = CARD_BARE_URL.exec(text.slice(i));
  if (!match) return null;
  const rawURL = match[0];
  if (i >= 2 && text.slice(i - 2, i) === "](") {
    return { kind: "text", text: rawURL, consumed: rawURL.length };
  }
  return { kind: "link", href: rawURL, label: rawURL, consumed: rawURL.length };
}

// renderCardText renders a card body field: markdown links and bare http(s) URLs
// become anchors, everything else is literal text.
//
// The loop advances one whole code point at a time, so a multi-byte character
// (and an astral-plane one, which is two UTF-16 units) is never split.
export function renderCardText(value: string | null | undefined): ReactNode {
  if (typeof value !== "string" || value === "") return value ?? "";
  const nodes: ReactNode[] = [];
  let i = 0;
  while (i < value.length) {
    const link = cardLinkAt(value, i);
    if (link) {
      nodes.push(
        link.kind === "link" ? (
          <a key={nodes.length} href={link.href} target="_blank" rel="noopener noreferrer">
            {link.label}
          </a>
        ) : (
          link.text
        ),
      );
      i += link.consumed;
      continue;
    }
    const url = cardBareURLAt(value, i);
    if (url) {
      nodes.push(
        url.kind === "link" ? (
          <a key={nodes.length} href={url.href} target="_blank" rel="noopener noreferrer">
            {url.label}
          </a>
        ) : (
          url.text
        ),
      );
      i += url.consumed;
      continue;
    }
    const width = (value.codePointAt(i) ?? 0) > 0xffff ? 2 : 1;
    nodes.push(value.slice(i, i + width));
    i += width;
  }
  return <>{nodes}</>;
}
