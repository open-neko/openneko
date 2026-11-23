"use strict";

const x = require("./ooxml.cjs");
const T = require("./template.cjs");
const B = require("./build.cjs");
const { relsOf, paragraphText } = require("./deck.cjs");

const MAX_CHARS = 60000;

function cmOf(emu) {
  return Math.round((emu / T.EMU_PER_CM) * 100) / 100;
}

function boxCm(box) {
  return box ? { x: cmOf(box.x), y: cmOf(box.y), w: cmOf(box.w), h: cmOf(box.h) } : undefined;
}

function parseSlides(value, count) {
  if (value === undefined) return Array.from({ length: count }, (_, i) => i + 1);
  const out = new Set();
  for (const piece of String(value).split(",")) {
    const match = /^\s*(\d+)\s*(?:-\s*(\d*)\s*)?$/.exec(piece);
    if (!match) throw new Error(`--slides takes numbers and ranges such as 2-4,7; got ${JSON.stringify(value)}`);
    const from = Number(match[1]);
    const to = match[2] === undefined ? from : match[2] === "" ? count : Number(match[2]);
    if (from < 1 || to > count || from > to) throw new Error(`the slides must be 1 to ${count}; got ${JSON.stringify(piece.trim())}`);
    for (let n = from; n <= to; n++) out.add(n);
  }
  return [...out].sort((a, b) => a - b);
}

function hasChild(node, name) {
  return Boolean(node && x.child(node, name));
}

function levelStyle(lstStyle, level) {
  return lstStyle ? x.child(lstStyle, `a:lvl${level + 1}pPr`) : undefined;
}

function bulletFor(p, level, listDefault, inherited) {
  const pPr = x.child(p, "a:pPr");
  if (hasChild(pPr, "a:buNone")) return null;
  if (hasChild(pPr, "a:buAutoNum")) return "1.";
  if (hasChild(pPr, "a:buChar") || hasChild(pPr, "a:buBlip")) return "-";
  for (const style of inherited) {
    const lvl = levelStyle(style, level);
    if (hasChild(lvl, "a:buNone")) return null;
    if (hasChild(lvl, "a:buAutoNum")) return "1.";
    if (hasChild(lvl, "a:buChar")) return "-";
  }
  return listDefault ? "-" : null;
}

function escapeMd(text) {
  return text.replace(/([\\*_`[\]|])/g, "\\$1");
}

function runFormat(run, links) {
  const rPr = x.child(run, "a:rPr");
  const a = rPr ? x.attrs(rPr) : {};
  const link = rPr && x.child(rPr, "a:hlinkClick");
  return { bold: a.b === "1" || a.b === "true", italic: a.i === "1" || a.i === "true", url: (link && links.get(x.attrs(link)["r:id"])) || "" };
}

function spanMarkdown(span) {
  const text = escapeMd(span.text);
  const trimmed = text.trim();
  if (!trimmed) return text;
  const lead = text.slice(0, text.indexOf(trimmed[0]));
  const tail = text.slice(lead.length + trimmed.length);
  let core = trimmed;
  if (span.bold) core = `**${core}**`;
  if (span.italic) core = `*${core}*`;
  if (span.url) core = `[${core}](${span.url})`;
  return `${lead}${core}${tail}`;
}

function paragraphMarkdown(p, links) {
  const spans = [];
  for (const node of x.kids(p)) {
    const name = x.tagOf(node);
    let span;
    if (name === "a:r") span = { text: x.textOf(x.child(node, "a:t")), ...runFormat(node, links) };
    else if (name === "a:fld") span = { text: x.textOf(x.child(node, "a:t")), bold: false, italic: false, url: "" };
    else if (name === "a:br") span = { text: "", br: true };
    else continue;
    const last = spans[spans.length - 1];
    if (last && !last.br && !span.br && last.bold === span.bold && last.italic === span.italic && last.url === span.url) last.text += span.text;
    else spans.push(span);
  }
  return spans.map((span) => (span.br ? "<br>" : spanMarkdown(span))).join("");
}

function paragraphsOf(txBody, links, listDefault, inherited) {
  if (!txBody) return [];
  const own = x.child(txBody, "a:lstStyle");
  return x.children(txBody, "a:p").map((p) => {
    const pPr = x.child(p, "a:pPr");
    const level = pPr && x.attrs(pPr).lvl ? Number(x.attrs(pPr).lvl) : 0;
    return { level, text: paragraphText(p), markdown: paragraphMarkdown(p, links), bullet: bulletFor(p, level, listDefault, [own, ...inherited].filter(Boolean)) };
  });
}

function tableOf(frame) {
  const tbl = x.child(x.child(x.child(frame, "a:graphic"), "a:graphicData"), "a:tbl");
  if (!tbl) return undefined;
  return x.children(tbl, "a:tr").map((tr) => x.children(tr, "a:tc").map((tc) => {
    const a = x.attrs(tc);
    if (a.hMerge === "1" || a.vMerge === "1") return null;
    return x.children(x.child(tc, "a:txBody") ?? x.el("a:txBody"), "a:p").map(paragraphText).join("\n");
  }));
}

function cacheValues(node) {
  const cache = node && (x.child(node, "c:strCache") ?? x.child(node, "c:numCache"));
  if (!cache) {
    const lit = node && (x.child(node, "c:strLit") ?? x.child(node, "c:numLit"));
    if (!lit) return [];
    return valuesOf(lit);
  }
  return valuesOf(cache);
}

function valuesOf(cache) {
  const count = Number(x.attrs(x.child(cache, "c:ptCount") ?? x.el("c:ptCount", { val: 0 })).val) || 0;
  const out = Array(count).fill(null);
  for (const pt of x.children(cache, "c:pt")) out[Number(x.attrs(pt).idx)] = x.textOf(x.child(pt, "c:v"));
  return out;
}

function refValues(node) {
  if (!node) return [];
  for (const inner of ["c:strRef", "c:numRef", "c:multiLvlStrRef"]) {
    const ref = x.child(node, inner);
    if (ref) {
      if (inner === "c:multiLvlStrRef") {
        const cache = x.child(ref, "c:multiLvlStrCache");
        const lvl = cache && x.child(cache, "c:lvl");
        if (!cache || !lvl) return [];
        const count = Number(x.attrs(x.child(cache, "c:ptCount")).val) || 0;
        const out = Array(count).fill(null);
        for (const pt of x.children(lvl, "c:pt")) out[Number(x.attrs(pt).idx)] = x.textOf(x.child(pt, "c:v"));
        return out;
      }
      return cacheValues(ref);
    }
  }
  return cacheValues(node);
}

function chartPart(deck, slidePath, frame) {
  const graphic = x.tagOf(frame) === "p:graphicFrame" && x.child(frame, "a:graphic");
  const data = graphic && x.child(graphic, "a:graphicData");
  const ref = data && x.child(data, "c:chart");
  if (!ref) return undefined;
  const rel = relsOf(deck.pkg, slidePath).find((entry) => entry.id === x.attrs(ref)["r:id"]);
  return rel?.part && deck.pkg.files.has(rel.part) ? rel.part : undefined;
}

function chartOf(deck, slidePath, frame) {
  const path = chartPart(deck, slidePath, frame);
  if (!path) return undefined;
  const chart = x.child(x.rootOf(deck.pkg.xml(path)), "c:chart");
  if (!chart) return undefined;
  const plotArea = x.child(chart, "c:plotArea");
  const plots = x.kids(plotArea).filter((node) => /Chart$/.test(x.tagOf(node) ?? ""));
  const kinds = plots.map((plot) => {
    const name = x.tagOf(plot).slice(2, -5);
    const dir = x.child(plot, "c:barDir");
    const grouping = x.child(plot, "c:grouping");
    const g = grouping ? x.attrs(grouping).val : undefined;
    if (name === "bar") return `${g === "stacked" || g === "percentStacked" ? `${g} ` : ""}${dir && x.attrs(dir).val === "bar" ? "bar" : "column"}`;
    return name;
  });
  const series = [];
  let categories = [];
  for (const plot of plots) {
    for (const ser of x.children(plot, "c:ser")) {
      const cat = x.child(ser, "c:cat") ?? x.child(ser, "c:xVal");
      const val = x.child(ser, "c:val") ?? x.child(ser, "c:yVal");
      const name = refValues(x.child(ser, "c:tx"))[0] ?? `Series ${series.length + 1}`;
      if (!categories.length) categories = refValues(cat);
      series.push({ name, values: refValues(val).map((v) => (v === null || v === "" ? null : Number(v))) });
    }
  }
  const titleNode = x.child(chart, "c:title");
  const title = titleNode ? x.children(x.child(x.child(titleNode, "c:tx") ?? x.el("c:tx"), "c:rich") ?? x.el("c:rich"), "a:p").map(paragraphText).join(" ").trim() : "";
  return { part: path, type: kinds.join(" + ") || "unknown", title: title || undefined, categories, series };
}

function pictureOf(deck, slidePath, pic, rels) {
  const blip = x.child(x.child(pic, "p:blipFill") ?? x.el("p:blipFill"), "a:blip");
  const rel = blip && rels.find((entry) => entry.id === x.attrs(blip)["r:embed"]);
  return { media: rel?.part, linked: rel?.mode === "External" ? rel.target : undefined };
}

function kindOf(shape) {
  const tag = x.tagOf(shape);
  if (tag === "p:pic") return "picture";
  if (tag === "p:grpSp") return "group";
  if (tag === "p:cxnSp") return "connector";
  if (tag === "p:graphicFrame") {
    const data = x.child(x.child(shape, "a:graphic"), "a:graphicData");
    const uri = data ? x.attrs(data).uri ?? "" : "";
    if (uri.endsWith("/table")) return "table";
    if (uri.endsWith("/chart")) return "chart";
    if (uri.endsWith("/diagram")) return "diagram";
    if (/ole/i.test(uri)) return "object";
    return "frame";
  }
  return "text";
}

function layoutStyleFor(deck, layoutPath, ph) {
  if (!layoutPath || !ph) return [];
  const styles = [];
  const fromLayout = B.matchPh(B.placeholdersIn(deck, layoutPath), ph, false);
  const lst = fromLayout && x.child(x.child(fromLayout.shape, "p:txBody") ?? x.el("p:txBody"), "a:lstStyle");
  if (lst) styles.push(lst);
  return styles;
}

function describe(deck, slide, layoutPath, shape, rels, links) {
  const cNvPr = B.cNvPrOf(shape);
  const a = cNvPr ? x.attrs(cNvPr) : {};
  const ph = B.phOf(shape);
  const kind = kindOf(shape);
  const out = { id: Number(a.id), name: a.name ?? "", kind };
  if (ph) out.placeholder = ph.idx !== undefined ? `${ph.type} ${ph.idx}` : ph.type;
  if (a.descr) out.alt = a.descr;
  if (a.hidden === "1") out.hidden = true;
  const box = layoutPath && ph ? B.resolvedBox(deck, layoutPath, ph, B.boxOf(shape)) : B.boxOf(shape);
  if (box) out.box = boxCm(box);
  if (kind === "text" || kind === "connector") {
    const listDefault = Boolean(ph && B.CONTENT.has(ph.type) && ph.type !== "subTitle");
    const paragraphs = paragraphsOf(x.child(shape, "p:txBody"), links, listDefault, layoutStyleFor(deck, layoutPath, ph));
    if (paragraphs.some((p) => p.text.trim())) out.paragraphs = paragraphs;
  } else if (kind === "table") out.table = tableOf(shape);
  else if (kind === "chart") out.chart = chartOf(deck, slide.path, shape);
  else if (kind === "picture") Object.assign(out, pictureOf(deck, slide.path, shape, rels));
  else if (kind === "group") out.shapes = B.treeShapes(shape).map((inner) => describe(deck, slide, null, inner, rels, links));
  return out;
}

function slideInfo(deck, slide, layoutsByPath) {
  const rels = relsOf(deck.pkg, slide.path);
  const links = new Map(rels.filter((rel) => rel.type === T.REL_TYPES.hyperlink).map((rel) => [rel.id, rel.target]));
  const layoutPath = deck.layoutOf(slide.path);
  const shapes = B.treeShapes(deck.spTree(slide.path)).map((shape) => describe(deck, slide, layoutPath, shape, rels, links));
  const titleShape = shapes.find((shape) => shape.placeholder && /^(title|ctrTitle)\b/.test(shape.placeholder));
  const notes = deck.notesText(slide.path);
  const root = deck.slideRoot(slide.path);
  return {
    number: slide.number,
    layout: layoutsByPath.get(layoutPath)?.name,
    title: titleShape?.paragraphs?.map((p) => p.text).join(" ").trim() || undefined,
    hidden: x.attrs(root).show === "0" || undefined,
    shapes,
    notes: notes?.text,
  };
}

function flatten(shapes) {
  return shapes.flatMap((shape) => (shape.shapes ? [shape, ...flatten(shape.shapes)] : [shape]));
}

function layoutsInfo(deck) {
  return deck.layouts().map((layout) => ({
    number: layout.number,
    name: layout.name,
    type: layout.type,
    master: layout.master,
    placeholders: B.layoutPlaceholders(deck, layout.path)
      .filter((ph) => !["dt", "ftr", "sldNum"].includes(ph.type))
      .map((ph) => ({ idx: ph.idx ?? 0, type: ph.type, name: ph.name, box: boxCm(ph.box) })),
  }));
}

function info(deck) {
  const size = deck.size();
  const layouts = deck.layouts();
  const byPath = new Map(layouts.map((layout) => [layout.path, layout]));
  const slides = deck.slides().map((slide) => {
    const s = slideInfo(deck, slide, byPath);
    const all = flatten(s.shapes);
    const count = (kind) => all.filter((shape) => shape.kind === kind).length;
    return { number: s.number, title: s.title, layout: s.layout, hidden: s.hidden, shapes: all.length, pictures: count("picture") || undefined, tables: count("table") || undefined, charts: count("chart") || undefined, notes: s.notes ? true : undefined };
  });
  const ratio = size.cx / size.cy;
  const named = Object.entries(T.SLIDE_SIZES).find(([, [cx, cy]]) => Math.abs(cx / cy - ratio) < 0.01)?.[0];
  return {
    size: { w: cmOf(size.cx), h: cmOf(size.cy), ratio: named },
    properties: Object.fromEntries(Object.entries(deck.properties()).filter(([, v]) => v)),
    macros: deck.isMacroEnabled() || undefined,
    slides,
    layouts: layouts.map((layout) => `${layout.number} ${layout.name}`),
  };
}

function tableMarkdown(rows) {
  if (!rows?.length) return "";
  const width = Math.max(...rows.map((row) => row.length));
  const cell = (value) => (value === null ? "" : escapeMd(value).replace(/\n/g, "<br>"));
  const lines = rows.map((row) => `| ${Array.from({ length: width }, (_, i) => cell(row[i] ?? "")).join(" | ")} |`);
  lines.splice(1, 0, `|${" --- |".repeat(width)}`);
  return lines.join("\n");
}

function chartMarkdown(chart) {
  if (!chart) return "Chart (no data)";
  const head = `Chart: ${chart.type}${chart.title ? `, "${chart.title}"` : ""}`;
  const rows = [["", ...chart.series.map((s) => s.name)], ...chart.categories.map((c, i) => [c ?? "", ...chart.series.map((s) => (s.values[i] === null || s.values[i] === undefined ? "" : String(s.values[i])))])];
  return chart.categories.length ? `${head}\n\n${tableMarkdown(rows)}` : head;
}

function shapeMarkdown(shape, options) {
  const tag = options.ids ? `[#${shape.id} ${shape.name}${shape.placeholder ? `, ${shape.placeholder}` : ""}] ` : "";
  if (shape.kind === "group") return shape.shapes.map((inner) => shapeMarkdown(inner, options)).filter(Boolean).join("\n\n");
  if (shape.kind === "picture") return `${tag}![${escapeMd(shape.alt ?? "")}](${shape.media ?? shape.linked ?? ""})`;
  if (shape.kind === "table") return tag ? `${tag.trimEnd()}\n${tableMarkdown(shape.table)}` : tableMarkdown(shape.table);
  if (shape.kind === "chart") return `${tag}${chartMarkdown(shape.chart)}`;
  if (shape.kind === "diagram" || shape.kind === "object" || shape.kind === "frame") return `${tag}(${shape.kind})`;
  if (!shape.paragraphs) return options.ids && shape.placeholder ? `${tag}(empty)` : "";
  let number = 0;
  const lines = shape.paragraphs.filter((p) => p.text.trim() || p.bullet === null).map((p) => {
    if (!p.text.trim()) return "";
    const indent = "  ".repeat(p.level);
    if (p.bullet === "1.") return `${indent}${++number}. ${p.markdown}`;
    number = 0;
    return p.bullet ? `${indent}- ${p.markdown}` : p.markdown;
  });
  const text = lines.join("\n").replace(/\n{3,}/g, "\n\n").trim();
  return tag && text.includes("\n") ? `${tag.trimEnd()}\n${text}` : `${tag}${text}`;
}

function slideMarkdown(s, options) {
  const head = `## Slide ${s.number}${s.title ? `: ${s.title}` : ""}`;
  const meta = [s.layout ? `layout "${s.layout}"` : "", s.hidden ? "hidden" : ""].filter(Boolean).join(", ");
  const body = s.shapes
    .filter((shape) => options.ids || !(shape.placeholder && /^(title|ctrTitle|dt|ftr|sldNum)\b/.test(shape.placeholder)))
    .map((shape) => shapeMarkdown(shape, options))
    .filter(Boolean);
  const parts = [meta ? `${head}\n(${meta})` : head, ...body];
  if (s.notes && options.notes !== false) parts.push(`Notes:\n${s.notes.split("\n").map((line) => `> ${line}`).join("\n")}`);
  return parts.join("\n\n");
}

function slideText(s, options) {
  const lines = [`Slide ${s.number}`];
  const walk = (shapes) => {
    for (const shape of shapes) {
      if (shape.shapes) walk(shape.shapes);
      else if (shape.paragraphs) lines.push(...shape.paragraphs.map((p) => p.text).filter((t) => t.trim()));
      else if (shape.table) lines.push(...shape.table.map((row) => row.filter((c) => c !== null).join("\t")));
      else if (shape.chart) lines.push(chartMarkdown(shape.chart));
    }
  };
  walk(s.shapes);
  if (s.notes && options.notes !== false) lines.push(`Notes: ${s.notes}`);
  return lines.join("\n");
}

function readDeck(deck, options = {}) {
  const all = deck.slides();
  const wanted = parseSlides(options.slides, all.length);
  const byPath = new Map(deck.layouts().map((layout) => [layout.path, layout]));
  const format = options.format ?? "markdown";
  const max = options.maxChars ?? MAX_CHARS;
  const out = [];
  let used = 0;
  for (const number of wanted) {
    const s = slideInfo(deck, all[number - 1], byPath);
    if (format === "json") {
      for (const shape of flatten(s.shapes)) for (const p of shape.paragraphs ?? []) delete p.markdown;
      out.push(s);
      used += JSON.stringify(s).length;
    } else {
      const text = format === "text" ? slideText(s, options) : slideMarkdown(s, options);
      out.push(text);
      used += text.length;
    }
    if (used > max && number !== wanted[wanted.length - 1]) {
      const next = wanted[wanted.indexOf(number) + 1];
      const note = `The output stops at slide ${number} of ${all.length}. Run read with --slides ${next}- to see the rest.`;
      if (format === "json") return { slides: out, more: note };
      return `${out.join("\n\n")}\n\n${note}`;
    }
  }
  if (format === "json") return { slides: out };
  return out.join("\n\n") || "(no slides)";
}

module.exports = { chartOf, info, layoutsInfo, parseSlides, readDeck, slideInfo, tableOf };
