// The page as text a model reads, every control numbered: each is stamped
// data-oa-ref, which a click or a typed value finds it by until the next
// snapshot numbers the page again. Written for Chrome and for Lightpanda,
// which runs scripts but lays nothing out, so nothing here asks where an
// element is drawn.
(() => {
  const max = 200000;
  const skip = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE", "SVG", "HEAD", "IFRAME", "CANVAS"]);
  const block = new Set([
    "ADDRESS", "ARTICLE", "ASIDE", "BLOCKQUOTE", "BR", "DD", "DIV", "DL", "DT", "FIELDSET", "FIGCAPTION",
    "FIGURE", "FOOTER", "FORM", "H1", "H2", "H3", "H4", "H5", "H6", "HEADER", "HR", "LI", "MAIN", "NAV",
    "OL", "P", "PRE", "SECTION", "TABLE", "TR", "UL",
  ]);
  const roles = new Set(["button", "link", "checkbox", "radio", "tab", "menuitem", "option", "switch", "textbox", "combobox"]);
  const out = [];
  let size = 0;
  let ref = 0;
  const put = (s) => {
    if (size < max) {
      out.push(s);
      size += s.length;
    }
  };
  const flat = (s, n) => {
    s = (s || "").replace(/\s+/g, " ").trim();
    return s.length > n ? s.slice(0, n) + "…" : s;
  };
  const hidden = (el) => {
    if (el.hidden || el.getAttribute("aria-hidden") === "true") return true;
    if (el.style && (el.style.display === "none" || el.style.visibility === "hidden")) return true;
    try {
      const cs = window.getComputedStyle && window.getComputedStyle(el);
      return !!cs && (cs.display === "none" || cs.visibility === "hidden");
    } catch (e) {
      return false;
    }
  };
  const kind = (el) => {
    const tag = el.tagName;
    const role = (el.getAttribute("role") || "").toLowerCase();
    if (tag === "A" && el.hasAttribute("href")) return "link";
    if (tag === "BUTTON" || tag === "SUMMARY") return "button";
    if (tag === "SELECT") return "select";
    if (tag === "TEXTAREA") return "field textarea";
    if (tag === "INPUT") {
      const type = (el.getAttribute("type") || "text").toLowerCase();
      if (type === "hidden") return "";
      if (["submit", "button", "reset", "image"].includes(type)) return "button";
      if (type === "checkbox" || type === "radio") return type;
      return "field " + type;
    }
    if (roles.has(role)) return role;
    if (el.isContentEditable) return "field text";
    if (el.hasAttribute("onclick")) return "button";
    return "";
  };
  const label = (el, k) => {
    const named = el.getAttribute("aria-label") || el.getAttribute("title") || "";
    if (k.startsWith("field") || k === "select" || k === "checkbox" || k === "radio") {
      const name = el.getAttribute("name") || el.getAttribute("placeholder") || named;
      let s = name ? '"' + flat(name, 60) + '"' : "";
      if (k === "select") {
        const o = el.options && el.options[el.selectedIndex];
        const all = Array.from(el.options || []).slice(0, 20).map((x) => flat(x.textContent, 40));
        s += " = " + JSON.stringify(o ? flat(o.textContent, 40) : "") + " of " + JSON.stringify(all);
      } else if (k === "checkbox" || k === "radio") {
        s += el.checked ? " checked" : "";
      } else if (k !== "field password") {
        s += " = " + JSON.stringify(flat(el.value, 200));
      }
      return s.trim();
    }
    const text = flat(el.textContent, 100) || flat(el.getAttribute("value"), 100) || flat(named, 100);
    if (k === "link") {
      const href = el.getAttribute("href") || "";
      return text + (href && !href.startsWith("javascript:") ? " (" + flat(href, 120) + ")" : "");
    }
    return text || flat(el.getAttribute("alt"), 100);
  };
  const walk = (node) => {
    if (size >= max) return;
    if (node.nodeType === 3) {
      const t = node.textContent.replace(/\s+/g, " ");
      if (t.trim()) put(t);
      return;
    }
    if (node.nodeType !== 1) return;
    const el = node;
    if (skip.has(el.tagName.toUpperCase()) || hidden(el)) return;
    const k = kind(el);
    if (k) {
      ref++;
      el.setAttribute("data-oa-ref", String(ref));
      put(" [" + ref + " " + k + "] " + label(el, k) + " ");
      return;
    }
    const isBlock = block.has(el.tagName);
    const h = /^H([1-6])$/.exec(el.tagName);
    if (isBlock) put("\n");
    if (h) put("#".repeat(Number(h[1])) + " ");
    if (el.tagName === "LI") put("- ");
    if (el.tagName === "IMG" && el.getAttribute("alt")) put("[image: " + flat(el.getAttribute("alt"), 100) + "]");
    for (const c of el.childNodes) walk(c);
    if (isBlock) put("\n");
  };
  for (const old of document.querySelectorAll("[data-oa-ref]")) old.removeAttribute("data-oa-ref");
  if (document.body) walk(document.body);
  const text = out
    .join("")
    .split("\n")
    .map((l) => l.replace(/ +/g, " ").trim())
    .filter((l, i, a) => l || (i > 0 && a[i - 1]))
    .join("\n")
    .trim();
  return { url: location.href, title: document.title || "", text: text, cut: size >= max, refs: ref };
})()
