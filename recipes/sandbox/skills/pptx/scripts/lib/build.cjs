"use strict";

const x = require("./ooxml.cjs");
const T = require("./template.cjs");
const S = require("./shapes.cjs");

const NV = { "p:sp": "p:nvSpPr", "p:pic": "p:nvPicPr", "p:graphicFrame": "p:nvGraphicFramePr", "p:grpSp": "p:nvGrpSpPr", "p:cxnSp": "p:nvCxnSpPr" };
const TITLES = new Set(["title", "ctrTitle"]);
const FOOTERS = new Set(["dt", "ftr", "sldNum"]);
const CONTENT = new Set(["obj", "body", "chart", "tbl", "pic", "clipArt", "dgm", "media"]);

function nvOf(shape) {
  const tag = NV[x.tagOf(shape)];
  return tag ? x.child(shape, tag) : undefined;
}

function cNvPrOf(shape) {
  const nv = nvOf(shape);
  return nv ? x.child(nv, "p:cNvPr") : undefined;
}

function phOf(shape) {
  const nv = nvOf(shape);
  const nvPr = nv && x.child(nv, "p:nvPr");
  const ph = nvPr && x.child(nvPr, "p:ph");
  if (!ph) return undefined;
  const a = x.attrs(ph);
  return { type: a.type ?? "obj", idx: a.idx !== undefined ? Number(a.idx) : undefined, node: ph };
}

function boxOf(shape) {
  const holder = x.tagOf(shape) === "p:graphicFrame" ? shape : x.child(shape, x.tagOf(shape) === "p:grpSp" ? "p:grpSpPr" : "p:spPr");
  const xfrm = holder && x.child(holder, x.tagOf(shape) === "p:graphicFrame" ? "p:xfrm" : "a:xfrm");
  const off = xfrm && x.child(xfrm, "a:off");
  const ext = xfrm && x.child(xfrm, "a:ext");
  if (!off || !ext) return undefined;
  const o = x.attrs(off);
  const e = x.attrs(ext);
  return { x: Number(o.x), y: Number(o.y), w: Number(e.cx), h: Number(e.cy) };
}

function masterKind(type) {
  if (TITLES.has(type)) return "title";
  if (FOOTERS.has(type) || type === "sldImg" || type === "hdr") return type;
  return "body";
}

function matchPh(list, ph, master) {
  if (master) return list.find((entry) => masterKind(entry.ph.type) === masterKind(ph.type));
  if (ph.idx !== undefined && ph.idx !== 0) {
    const byIdx = list.find((entry) => (entry.ph.idx ?? 0) === ph.idx);
    if (byIdx) return byIdx;
  }
  return list.find((entry) => entry.ph.type === ph.type) ?? (TITLES.has(ph.type) ? list.find((entry) => TITLES.has(entry.ph.type)) : undefined);
}

function treeShapes(tree) {
  const out = [];
  if (!tree) return out;
  for (const node of x.kids(tree)) {
    const tag = x.tagOf(node);
    if (NV[tag]) out.push(node);
    else if (tag === "mc:AlternateContent") {
      const choice = x.child(node, "mc:Choice") ?? x.child(node, "mc:Fallback");
      if (choice) for (const inner of x.kids(choice)) if (NV[x.tagOf(inner)]) out.push(inner);
    }
  }
  return out;
}

function placeholdersIn(deck, path) {
  const root = x.rootOf(deck.pkg.xml(path));
  const tree = x.child(x.child(root, "p:cSld"), "p:spTree");
  return treeShapes(tree).map((shape) => ({ shape, ph: phOf(shape) })).filter((entry) => entry.ph);
}

function resolvedBox(deck, layoutPath, ph, own) {
  if (own) return own;
  const fromLayout = matchPh(placeholdersIn(deck, layoutPath), ph, false);
  const layoutBox = fromLayout && boxOf(fromLayout.shape);
  if (layoutBox) return layoutBox;
  const masterPath = deck.masterOf(layoutPath);
  if (!masterPath) return undefined;
  const fromMaster = matchPh(placeholdersIn(deck, masterPath), fromLayout?.ph ?? ph, true);
  return fromMaster && boxOf(fromMaster.shape);
}

function layoutPlaceholders(deck, layoutPath) {
  return placeholdersIn(deck, layoutPath).map(({ shape, ph }) => {
    const a = {};
    if (ph.type !== "obj") a.type = ph.type;
    if (x.attrs(ph.node).idx !== undefined) a.idx = x.attrs(ph.node).idx;
    if (x.attrs(ph.node).sz !== undefined) a.sz = x.attrs(ph.node).sz;
    if (x.attrs(ph.node).orient !== undefined) a.orient = x.attrs(ph.node).orient;
    return { type: ph.type, idx: ph.idx, name: x.attrs(cNvPrOf(shape)).name ?? "", box: resolvedBox(deck, layoutPath, ph, boxOf(shape)), phAttrs: a };
  });
}

class Ids {
  constructor(tree) {
    let max = 1;
    x.kids(tree).forEach(function visit(node) {
      const tag = x.tagOf(node);
      if (!tag || tag === "#text") return;
      if (tag === "p:cNvPr") max = Math.max(max, Number(x.attrs(node).id) || 0);
      const list = node[tag];
      if (Array.isArray(list)) list.forEach(visit);
    });
    this.last = max;
  }

  next() {
    this.last += 1;
    return this.last;
  }
}

function contextFor(deck, slidePath) {
  const tree = deck.spTree(slidePath);
  return { deck, part: slidePath, ids: new Ids(tree), tree };
}

function textOptions(content) {
  if (content && typeof content === "object" && !Array.isArray(content) && "text" in content && content.image === undefined && content.table === undefined && content.chart === undefined) {
    const { text, ...options } = content;
    return { text, options };
  }
  return { text: content, options: {} };
}

function bodyOptions(options) {
  const bodyPr = {};
  if (options.valign !== undefined) {
    const anchor = { top: "t", middle: "ctr", center: "ctr", bottom: "b" }[options.valign];
    if (!anchor) throw new Error("valign must be top, middle or bottom");
    bodyPr.anchor = anchor;
  }
  return { ...options, bodyPr };
}

function textPlaceholder(ph, content, ctx) {
  const { text, options } = textOptions(content);
  const id = ctx.ids.next();
  const nv = x.el("p:nvSpPr", null, [x.el("p:cNvPr", { id, name: `${(ph.name || "Placeholder").replace(/\s*\d+$/, "")} ${id - 1}` }), x.el("p:cNvSpPr", null, [x.el("a:spLocks", { noGrp: 1 })]), x.el("p:nvPr", null, [x.el("p:ph", ph.phAttrs)])]);
  return x.el("p:sp", null, [nv, x.el("p:spPr"), S.txBody(text ?? "", ctx, bodyOptions(options))]);
}

function fieldPlaceholder(ph, ctx, text, field) {
  const id = ctx.ids.next();
  const nv = x.el("p:nvSpPr", null, [x.el("p:cNvPr", { id, name: `${(ph.name || "Placeholder").replace(/\s*\d+$/, "")} ${id - 1}` }), x.el("p:cNvSpPr", null, [x.el("a:spLocks", { noGrp: 1 })]), x.el("p:nvPr", null, [x.el("p:ph", ph.phAttrs)])]);
  const run = field
    ? x.el("a:fld", { id: "{8A5C3F2E-6B1D-4E7A-9C0F-2D4B6E8A1C3F}", type: field }, [x.el("a:rPr", { lang: "en-US" }), x.el("a:t", null, [x.textNode(text)])])
    : x.el("a:r", null, [x.el("a:rPr", { lang: "en-US", dirty: 0 }), x.el("a:t", null, [x.textNode(text)])]);
  return x.el("p:sp", null, [nv, x.el("p:spPr"), x.el("p:txBody", null, [x.el("a:bodyPr"), x.el("a:lstStyle"), x.el("a:p", null, [run])])]);
}

function fillPlaceholder(ph, content, ctx) {
  if (content && typeof content === "object" && !Array.isArray(content)) {
    if (content.image !== undefined) return S.pictureShape(content, ctx, ph.box, ph);
    if (content.table !== undefined) {
      if (!ph.box) throw new Error(`placeholder "${ph.name}" has no size to hold a table`);
      return S.tableShape(content, ctx, ph.box);
    }
    if (content.chart !== undefined) {
      if (!ph.box) throw new Error(`placeholder "${ph.name}" has no size to hold a chart`);
      return S.chartShape(content, ctx, ph.box);
    }
  }
  return textPlaceholder(ph, content, ctx);
}

function contentPlaceholders(list) {
  return list.filter((ph) => CONTENT.has(ph.type)).sort((a, b) => (a.box?.x ?? 0) - (b.box?.x ?? 0) || (a.box?.y ?? 0) - (b.box?.y ?? 0) || (a.idx ?? 0) - (b.idx ?? 0));
}

function inferLayout(deck, spec) {
  const has = (key) => spec[key] !== undefined;
  let type;
  if (has("subtitle") && !has("body")) type = "title";
  else if (has("left") || has("right")) type = "twoObj";
  else if (has("section")) type = "secHead";
  else if (has("body")) type = "obj";
  else if (has("title")) type = "titleOnly";
  else type = "blank";
  const layouts = deck.layouts();
  return layouts.find((l) => l.type === type) ?? deck.findLayout(undefined);
}

function assignContent(spec, placeholders) {
  const used = new Set();
  const plan = [];
  const take = (ph, content, label) => {
    if (!ph) throw new Error(`the layout has no placeholder for ${label}; use a layout that has one, or add the content as a shape`);
    used.add(ph);
    plan.push({ ph, content });
  };
  const unused = (list) => list.filter((ph) => !used.has(ph));
  const body = () => unused(contentPlaceholders(placeholders));
  const title = spec.title ?? spec.section;
  if (title !== undefined) take(placeholders.find((ph) => TITLES.has(ph.type)), title, "a title");
  if (spec.subtitle !== undefined) take(placeholders.find((ph) => ph.type === "subTitle") ?? body()[0], spec.subtitle, "a subtitle");
  if (spec.body !== undefined) take(body()[0], spec.body, "body content");
  if (spec.left !== undefined) take(body()[0], spec.left, "left content");
  if (spec.right !== undefined) take(body()[0], spec.right, "right content");
  if (spec.placeholders !== undefined) {
    if (!spec.placeholders || typeof spec.placeholders !== "object" || Array.isArray(spec.placeholders)) throw new Error("placeholders must be an object of placeholder index or name to content");
    for (const [key, content] of Object.entries(spec.placeholders)) {
      const ph = /^\d+$/.test(key) ? placeholders.find((entry) => entry.idx === Number(key)) : placeholders.find((entry) => entry.name.toLowerCase() === key.toLowerCase());
      if (!ph) throw new Error(`no placeholder "${key}" in the layout; it has ${placeholders.map((entry) => `${entry.idx ?? 0} "${entry.name}" (${entry.type})`).join(", ")}`);
      if (used.has(ph)) throw new Error(`placeholder "${key}" is filled twice`);
      take(ph, content, `"${key}"`);
    }
  }
  return plan;
}

function setBackground(deck, slidePath, color) {
  const cSld = x.child(deck.slideRoot(slidePath), "p:cSld");
  x.removeChildren(cSld, "p:bg");
  if (color === null || color === false) return;
  x.kids(cSld).unshift(x.el("p:bg", null, [x.el("p:bgPr", null, [S.solidFill(color, "background"), x.el("a:effectLst")])]));
}

function setHidden(deck, slidePath, hidden) {
  const a = x.attrs(deck.slideRoot(slidePath));
  if (hidden) a.show = "0";
  else delete a.show;
}

const SLIDE_KEYS = new Set(["layout", "title", "section", "subtitle", "body", "left", "right", "placeholders", "shapes", "notes", "background", "hidden", "footer", "slideNumber", "position"]);

function fillSlide(deck, path, layout, spec, defaults = {}) {
  const ctx = contextFor(deck, path);
  const placeholders = layoutPlaceholders(deck, layout.path);
  const nodes = assignContent(spec, placeholders).map(({ ph, content }) => fillPlaceholder(ph, content, ctx));
  const footer = spec.footer ?? defaults.footer;
  if (footer) {
    const ph = placeholders.find((entry) => entry.type === "ftr");
    if (ph) nodes.push(fieldPlaceholder(ph, ctx, String(footer)));
  }
  if (spec.slideNumber ?? defaults.slideNumbers) {
    const ph = placeholders.find((entry) => entry.type === "sldNum");
    const number = deck.slides().find((slide) => slide.path === path)?.number ?? 1;
    if (ph) nodes.push(fieldPlaceholder(ph, ctx, String(number), "slidenum"));
  }
  if (spec.shapes !== undefined) {
    if (!Array.isArray(spec.shapes)) throw new Error("shapes must be a list");
    for (const shape of spec.shapes) nodes.push(S.freeShape(shape, ctx));
  }
  x.kids(ctx.tree).push(...nodes);
  if (spec.background !== undefined) setBackground(deck, path, spec.background);
  if (spec.hidden !== undefined) setHidden(deck, path, spec.hidden);
  if (spec.notes !== undefined) deck.setNotes(path, spec.notes === null || spec.notes === "" ? [] : S.paragraphNodes(spec.notes, {}, {}));
}

function buildSlide(deck, spec, position, defaults) {
  if (!spec || typeof spec !== "object" || Array.isArray(spec)) throw new Error(`a slide must be an object, got ${JSON.stringify(spec)}`);
  for (const key of Object.keys(spec)) if (!SLIDE_KEYS.has(key)) throw new Error(`unknown slide field "${key}"; use ${[...SLIDE_KEYS].join(", ")}`);
  const layout = spec.layout !== undefined ? deck.findLayout(spec.layout) : inferLayout(deck, spec);
  const path = deck.addSlide(layout, position ?? spec.position);
  fillSlide(deck, path, layout, spec, defaults);
  return path;
}

module.exports = { CONTENT, NV, TITLES, boxOf, buildSlide, cNvPrOf, contextFor, fillPlaceholder, layoutPlaceholders, matchPh, phOf, placeholdersIn, resolvedBox, setBackground, setHidden, treeShapes };
