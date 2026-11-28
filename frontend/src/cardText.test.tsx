// cardText unit specs — the Go card-text_test.go table, ported.
//
// ⚠️ Every expected value is a hand-written literal, never one built with the
// same helper the code uses: a literal built by sharing the production escaper
// would agree with itself whatever that escaper produced.
//
// The assertions are made on the rendered DOM (textContent, href, target, rel)
// rather than on an HTML string, because React is the escaping boundary here:
// the Go version compared escaped HTML, while here the question is which
// characters became an anchor.
import { render } from "@testing-library/react";
import { renderCardText } from "./cardText";

interface Link {
  href: string;
  text: string;
}

function renderBody(input: string): HTMLElement {
  const { container } = render(<div data-testid="body">{renderCardText(input)}</div>);
  return container;
}

function anchors(container: HTMLElement): HTMLAnchorElement[] {
  return Array.from(container.querySelectorAll("a"));
}

describe("renderCardText", () => {
  describe.each<[string, string, string, Link[]]>([
    [
      "a markdown link, as an anchor carrying only the label",
      "[pane 87](http://127.0.0.1:1337/jump?pane=87&t=x)",
      "pane 87",
      [{ href: "http://127.0.0.1:1337/jump?pane=87&t=x", text: "pane 87" }],
    ],
    [
      "a bare https url, autolinked with the url as its own text",
      "see https://example.com/x",
      "see https://example.com/x",
      [{ href: "https://example.com/x", text: "https://example.com/x" }],
    ],
    [
      "a bare http url, autolinked",
      "go to http://127.0.0.1:18080/ now",
      "go to http://127.0.0.1:18080/ now",
      [{ href: "http://127.0.0.1:18080/", text: "http://127.0.0.1:18080/" }],
    ],
    [
      "a url inside prose parens, which must not swallow the closing paren",
      "see (https://example.com) for details",
      "see (https://example.com) for details",
      [{ href: "https://example.com", text: "https://example.com" }],
    ],
    [
      "an obsidian link, which the board's own navigation needs",
      "[task](obsidian://open?vault=Personal&file=25%20Tasks%2FFoo)",
      "task",
      [{ href: "obsidian://open?vault=Personal&file=25%20Tasks%2FFoo", text: "task" }],
    ],
    [
      "html inside a label, carried as literal text",
      "[<b>x</b>](https://example.com)",
      "<b>x</b>",
      [{ href: "https://example.com", text: "<b>x</b>" }],
    ],
    [
      "a quote in a url, which must not leave the href attribute",
      '[x](https://example.com/"onmouseover=evil)',
      "x",
      [{ href: 'https://example.com/"onmouseover=evil', text: "x" }],
    ],
  ])("renders %s", (_name, input, expectedText, expectedLinks) => {
    it("keeps the body text and links it as declared", () => {
      const container = renderBody(input);
      expect(container.textContent).toBe(expectedText);
      const found = anchors(container);
      expect(found).toHaveLength(expectedLinks.length);
      found.forEach((a, index) => {
        expect(a.getAttribute("href")).toBe(expectedLinks[index].href);
        expect(a.textContent).toBe(expectedLinks[index].text);
        // The tabnabbing guard: a new tab must not get a live window.opener.
        expect(a.getAttribute("target")).toBe("_blank");
        expect(a.getAttribute("rel")).toBe("noopener noreferrer");
      });
    });
  });

  describe.each<[string, string]>([
    ["a javascript link, refused by the scheme allowlist", "[x](javascript:alert(1))"],
    ["a data link, refused by the scheme allowlist", "[y](data:text/html,z)"],
    ["a mixed-case scheme, folded before the allowlist check", "[x](JaVaScRiPt:alert(1))"],
    ["a vbscript link, refused", "[x](vbscript:msgbox)"],
    ["a file link, refused", "[x](file:///etc/passwd)"],
    ["a scheme-relative url, refused", "[x](//example.com)"],
    ["a payload cut mid-link: no anchor and no autolinked fragment", "[pane 88](http://127.0.0.1:1337/"],
  ])("leaves %s as text", (_name, input) => {
    it("emits no anchor", () => {
      const container = renderBody(input);
      expect(anchors(container)).toHaveLength(0);
      expect(container.textContent).toBe(input);
    });
  });

  it("escapes html in the body with no live tag", () => {
    const container = renderBody("<script>alert(1)</script>");
    expect(container.querySelector("script")).toBeNull();
    expect(container.textContent).toBe("<script>alert(1)</script>");
  });

  it("renders an empty body as nothing", () => {
    const body = renderBody("");
    expect(body.textContent).toBe("");
    expect(body.querySelector('[data-testid="body"]')!.childNodes).toHaveLength(0);
  });

  it("leaves plain text with no link at all alone", () => {
    const container = renderBody("review: check the board");
    expect(anchors(container)).toHaveLength(0);
    expect(container.textContent).toBe("review: check the board");
  });

  it("keeps a multi-byte rune whole", () => {
    expect(renderBody("café — naïve").textContent).toBe("café — naïve");
    // An astral-plane character is two UTF-16 units; the loop must not split it.
    expect(renderBody("🚀 go").textContent).toBe("🚀 go");
  });

  it("autolinks the link that survived a cut but not the one the cut broke", () => {
    // The live reproduction: the producer stores exactly 200 characters, so the
    // first link is whole and the second is cut mid-URL. The whole link must
    // become an anchor and the broken one must stay text.
    const body =
      "review: Start Day in [pane 87](http://127.0.0.1:1337/jump?pane=87&t=abc), Check Prometheus Alerts in [pane 88](http://127.0.0.1:1337/";
    const container = renderBody(body);
    const found = anchors(container);
    expect(found).toHaveLength(1);
    expect(found[0].getAttribute("href")).toBe("http://127.0.0.1:1337/jump?pane=87&t=abc");
    expect(found[0].textContent).toBe("pane 87");
    expect(container.textContent).toContain("[pane 88](http://127.0.0.1:1337/");
  });
});
