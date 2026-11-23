"use strict";

const x = require("./ooxml.cjs");
const T = require("./template.cjs");
const S = require("./shapes.cjs");
const B = require("./build.cjs");
const { chartOf } = require("./read.cjs");
const { clone, relsOf, walk } = require("./deck.cjs");

const RPR_ORDER = ["a:ln", "a:noFill", "a:solidFill", "a:gradFill", "a:blipFill", "a:pattFill", "a:grpFill", "a:effectLst", "a:effectDag", "a:highlight", "a:uLnTx", "a:uLn", "a:uFillTx", "a:uFill", "a:latin", "a:ea", "a:cs", "a:sym", "a:hlinkClick", "a:hlinkMouseOver", "a:rtl", "a:extLst"];
const FILLS = ["a:noFill", "a:solidFill", "a:gradFill", "a:blipFill", "a:pattFill", "a:grpFill"];
const SER_ORDER = ["c:idx", "c:order", "c:tx", "c:spPr", "c:explosion", "c:invertIfNegative", "c:pictureOptions", "c:marker", "c:dPt", "c:dLbls", "c:trendline", "c:errBars", "c:cat", "c:val", "c:smooth", "c:shape", "c:extLst"];
const ROLES = { title: (ph) => B.TITLES.has(ph.type), subtitle: (ph) => ph.type === "subTitle" };

function normalize(text) {
  return text.replace(/[\u2018\u2019\u201A\u2032]/g, "'").replace(/[\u201C\u201D\u201E\u2033]/g, '"').replace(/\u00A0/g, " ");
}

function slidesFor(deck, value) {
  if (value === undefined) return deck.slides();
  const list = Array.isArray(value) ? value : [value];
  return list.map((n) => deck.slide(n));
}

function shapeEntries(container, out = []) {
  for (const node of x.kids(container)) {
    const tag = x.tagOf(node);
    if (B.NV[tag]) {
      out.push({ shape: node, container, top: node });
      if (tag === "p:grpSp") shapeEntries(node, out);
    } else if (tag === "mc:AlternateContent") {
      const choice = x.child(node, "mc:Choice") ?? x.child(node, "mc:Fallback");
      if (choice) for (const inner of x.kids(choice)) if (B.NV[x.tagOf(inner)]) out.push({ shape: inner, container, top: node });
    }
  }
  return out;
}

function shapeLabel(entry) {
  const a = x.attrs(B.cNvPrOf(entry.shape));
  const ph = B.phOf(entry.shape);
  return `${a.id} "${a.name ?? ""}"${ph ? ` (${ph.type})` : ""}`;
}

function bodyPlaceholders(entries) {
  return entries.filter((entry) => {
    const ph = B.phOf(entry.shape);
    return ph && B.CONTENT.has(ph.type);
  });
}

function findShape(deck, slide, wanted, required = true) {
  const entries = shapeEntries(deck.spTree(slide.path));
  let found;
  if (typeof wanted === "number" || /^\d+$/.test(String(wanted ?? ""))) found = entries.find((entry) => Number(x.attrs(B.cNvPrOf(entry.shape)).id) === Number(wanted));
  else if (typeof wanted === "string") {
    const key = wanted.trim().toLowerCase();
    if (ROLES[key]) found = entries.find((entry) => B.phOf(entry.shape) && ROLES[key](B.phOf(entry.shape)));
    else if (key === "body") found = bodyPlaceholders(entries).sort((a, b) => (B.boxOf(a.shape)?.x ?? 0) - (B.boxOf(b.shape)?.x ?? 0))[0];
    if (!found) {
      const named = entries.filter((entry) => (x.attrs(B.cNvPrOf(entry.shape)).name ?? "").toLowerCase() === key);
      if (named.length > 1) throw new Error(`slide ${slide.number} has ${named.length} shapes named "${wanted}"; use the shape id from read --ids`);
      found = named[0];
    }
  } else throw new Error(`shape must be a shape id, a shape name, or title, subtitle or body; got ${JSON.stringify(wanted)}`);
  if (!found && required) throw new Error(`slide ${slide.number} has no shape ${JSON.stringify(wanted)}; its shapes are ${entries.map(shapeLabel).join(", ") || "none"}`);
  return found;
}

function layoutRole(deck, slide, role) {
  const layoutPath = deck.layoutOf(slide.path);
  const list = layoutPath ? B.layoutPlaceholders(deck, layoutPath) : [];
  if (ROLES[role]) return list.find(ROLES[role]);
  if (role === "body") return list.filter((ph) => B.CONTENT.has(ph.type)).sort((a, b) => (a.box?.x ?? 0) - (b.box?.x ?? 0))[0];
  return undefined;
}

function mergeRPr(base, made) {
  if (!base) return made;
  const out = clone(base);
  const a = x.attrs(out);
  for (const [key, value] of Object.entries(x.attrs(made))) if (key !== "lang" && key !== "dirty") a[key] = value;
  a.dirty = "0";
  x.removeChildren(out, "a:hlinkClick");
  x.removeChildren(out, "a:hlinkMouseOver");
  for (const node of x.kids(made)) {
    const tag = x.tagOf(node);
    if (FILLS.includes(tag)) for (const fill of FILLS) x.removeChildren(out, fill);
    else x.removeChildren(out, tag);
    x.insertOrdered(out, node, RPR_ORDER);
  }
  return out;
}

function setParagraphs(txBody, content, ctx) {
  const { text, options } = typeof content === "object" && content !== null && !Array.isArray(content) && "text" in content ? (({ text: t, ...rest }) => ({ text: t, options: rest }))(content) : { text: content, options: {} };
  let baseRun;
  let basePPr;
  for (const p of x.children(txBody, "a:p")) {
    basePPr ??= x.child(p, "a:pPr");
    const run = x.child(p, "a:r");
    if (run) {
      baseRun = x.child(run, "a:rPr");
      break;
    }
  }
  const made = S.paragraphNodes(text ?? "", ctx, options);
  for (const p of made) {
    const list = x.kids(p);
    if (basePPr && x.tagOf(list[0]) !== "a:pPr") list.unshift(clone(basePPr));
    for (const node of list) {
      const tag = x.tagOf(node);
      if (tag !== "a:r" && tag !== "a:br") continue;
      const kids = x.kids(node);
      const at = kids.findIndex((entry) => x.tagOf(entry) === "a:rPr");
      if (at >= 0) kids[at] = mergeRPr(baseRun, kids[at]);
    }
  }
  x.removeChildren(txBody, "a:p");
  x.kids(txBody).push(...made);
}

function runText(run) {
  return x.textOf(x.child(run, "a:t"));
}

function setRunText(run, text) {
  let t = x.child(run, "a:t");
  if (!t) {
    t = x.el("a:t");
    x.kids(run).push(t);
  }
  t["a:t"] = text ? [x.textNode(text)] : [];
}

function groupsOf(p) {
  const groups = [[]];
  for (const node of x.kids(p)) {
    const tag = x.tagOf(node);
    if (tag === "a:r") groups[groups.length - 1].push(node);
    else if (tag === "a:br" || tag === "a:fld") groups.push([]);
  }
  return groups.filter((group) => group.length);
}

function findIn(text, find) {
  const hay = normalize(text);
  const needle = normalize(find);
  const out = [];
  let at = hay.indexOf(needle);
  while (at >= 0) {
    out.push(at);
    at = hay.indexOf(needle, at + needle.length);
  }
  return out;
}

function replaceAt(group, start, length, replacement) {
  const end = start + length;
  let offset = 0;
  let first = -1;
  let last = -1;
  const offsets = group.map((run) => {
    const here = offset;
    offset += runText(run).length;
    return here;
  });
  group.forEach((run, i) => {
    const len = runText(run).length;
    if (first < 0 && offsets[i] + len > start) first = i;
    if (offsets[i] < end) last = i;
  });
  const head = runText(group[first]).slice(0, start - offsets[first]);
  const tail = runText(group[last]).slice(end - offsets[last]);
  if (first === last) setRunText(group[first], head + replacement + tail);
  else {
    setRunText(group[first], head + replacement);
    for (let i = first + 1; i < last; i++) setRunText(group[i], "");
    setRunText(group[last], tail);
  }
}

function paragraphsIn(root) {
  const out = [];
  walk(root, (node) => {
    if (x.tagOf(node) === "a:p") {
      out.push(node);
      return false;
    }
  });
  return out;
}

function dropEmptyRuns(p) {
  const list = x.kids(p);
  for (let i = list.length - 1; i >= 0; i--) if (x.tagOf(list[i]) === "a:r" && !runText(list[i])) list.splice(i, 1);
  if (!list.some((node) => ["a:r", "a:br", "a:fld", "a:endParaRPr"].includes(x.tagOf(node)))) list.push(x.el("a:endParaRPr", { lang: "en-US", dirty: 0 }));
}

function replaceText(deck, op) {
  if (typeof op.find !== "string" || !op.find) throw new Error("replace needs find text");
  if (op.with === undefined || op.with === null) throw new Error("replace needs with text");
  const replacement = String(op.with);
  if (replacement.includes("\n")) throw new Error("with cannot hold a line break; use setText to write paragraphs");
  const hits = [];
  for (const slide of slidesFor(deck, op.slide)) {
    const roots = [{ root: deck.slideRoot(slide.path), where: `slide ${slide.number}` }];
    const notes = deck.notesPath(slide.path);
    if (op.notes && notes) roots.push({ root: x.rootOf(deck.pkg.xml(notes)), where: `the notes of slide ${slide.number}` });
    for (const { root, where } of roots) {
      for (const p of paragraphsIn(root)) {
        for (const group of groupsOf(p)) {
          const text = group.map(runText).join("");
          for (const start of findIn(text, op.find)) hits.push({ slide: slide.number, where, p, group, start });
        }
      }
    }
  }
  if (!hits.length) throw new Error(`no text "${op.find}" found${op.slide !== undefined ? ` on slide ${op.slide}` : ""}${op.notes ? "" : "; add \"notes\": true to search the notes too"}`);
  let chosen = hits;
  if (op.occurrence !== undefined) {
    const n = Number(op.occurrence);
    if (!Number.isInteger(n) || n < 1 || n > hits.length) throw new Error(`occurrence must be 1 to ${hits.length}`);
    chosen = [hits[n - 1]];
  } else if (hits.length > 1 && !op.all) {
    throw new Error(`"${op.find}" occurs ${hits.length} times (${[...new Set(hits.map((hit) => hit.where))].join(", ")}); add "all": true, an "occurrence", a "slide", or more text`);
  }
  const byGroup = new Map();
  for (const hit of chosen) {
    if (!byGroup.has(hit.group)) byGroup.set(hit.group, []);
    byGroup.get(hit.group).push(hit);
  }
  for (const [group, list] of byGroup) for (const hit of list.sort((a, b) => b.start - a.start)) replaceAt(group, hit.start, op.find.length, replacement);
  for (const p of new Set(chosen.map((hit) => hit.p))) dropEmptyRuns(p);
  return { replaced: chosen.length, slides: [...new Set(chosen.map((hit) => hit.slide))] };
}

function setText(deck, op) {
  const slide = deck.slide(op.slide);
  if (op.text === undefined) throw new Error("setText needs text");
  const entry = findShape(deck, slide, op.shape, typeof op.shape !== "string" || !(op.shape.toLowerCase() in ROLES || op.shape.toLowerCase() === "body"));
  const ctx = B.contextFor(deck, slide.path);
  if (!entry) {
    const ph = layoutRole(deck, slide, op.shape.toLowerCase());
    if (!ph) throw new Error(`slide ${slide.number} and its layout have no ${op.shape} placeholder`);
    x.kids(ctx.tree).push(B.fillPlaceholder(ph, op.text, ctx));
    return { slide: slide.number, created: op.shape };
  }
  let body = x.child(entry.shape, "p:txBody");
  if (!body) {
    if (x.tagOf(entry.shape) !== "p:sp") throw new Error(`shape ${shapeLabel(entry)} holds no text; use cell for a table, chart for a chart or image for a picture`);
    body = x.el("p:txBody", null, [x.el("a:bodyPr"), x.el("a:lstStyle")]);
    x.kids(entry.shape).push(body);
  }
  setParagraphs(body, op.text, ctx);
  return { slide: slide.number, shape: Number(x.attrs(B.cNvPrOf(entry.shape)).id) };
}

function tableOfShape(entry) {
  const tbl = x.tagOf(entry.shape) === "p:graphicFrame" && x.child(x.child(x.child(entry.shape, "a:graphic"), "a:graphicData"), "a:tbl");
  if (!tbl) throw new Error(`shape ${shapeLabel(entry)} is not a table`);
  return tbl;
}

function tableShapeOf(deck, slide, wanted) {
  if (wanted !== undefined) return findShape(deck, slide, wanted);
  const tables = shapeEntries(deck.spTree(slide.path)).filter((entry) => x.tagOf(entry.shape) === "p:graphicFrame" && x.child(x.child(x.child(entry.shape, "a:graphic"), "a:graphicData"), "a:tbl"));
  if (tables.length !== 1) throw new Error(`slide ${slide.number} has ${tables.length} tables; give the table's shape id`);
  return tables[0];
}

function index(value, max, what) {
  const n = Number(value);
  if (!Number.isInteger(n) || n < 1 || n > max) throw new Error(`${what} must be 1 to ${max}, got ${JSON.stringify(value)}`);
  return n - 1;
}

function setCell(deck, op) {
  const slide = deck.slide(op.slide);
  const tbl = tableOfShape(tableShapeOf(deck, slide, op.shape));
  const rows = x.children(tbl, "a:tr");
  const tr = rows[index(op.row, rows.length, "row")];
  const cells = x.children(tr, "a:tc");
  const tc = cells[index(op.column, cells.length, "column")];
  let body = x.child(tc, "a:txBody");
  if (!body) {
    body = x.el("a:txBody", null, [x.el("a:bodyPr"), x.el("a:lstStyle")]);
    x.kids(tc).unshift(body);
  }
  setParagraphs(body, op.text ?? "", B.contextFor(deck, slide.path));
  return { slide: slide.number };
}

function frameHeight(frame, delta) {
  const ext = x.child(x.child(frame, "p:xfrm"), "a:ext");
  if (ext) x.attrs(ext).cy = String(Math.max(0, Number(x.attrs(ext).cy) + delta));
}

function addRow(deck, op) {
  const slide = deck.slide(op.slide);
  const entry = tableShapeOf(deck, slide, op.shape);
  const tbl = tableOfShape(entry);
  const rows = x.children(tbl, "a:tr");
  const after = op.after === undefined ? rows.length - 1 : index(op.after, rows.length, "after");
  const source = rows[after];
  const row = clone(source);
  const cells = x.children(row, "a:tc");
  const values = Array.isArray(op.cells) ? op.cells : [];
  if (values.length > cells.length) throw new Error(`the table has ${cells.length} columns; cells has ${values.length}`);
  const ctx = B.contextFor(deck, slide.path);
  cells.forEach((tc, i) => {
    const a = x.attrs(tc);
    delete a.vMerge;
    delete a.rowSpan;
    let body = x.child(tc, "a:txBody");
    if (!body) {
      body = x.el("a:txBody", null, [x.el("a:bodyPr"), x.el("a:lstStyle")]);
      x.kids(tc).unshift(body);
    }
    setParagraphs(body, values[i] ?? "", ctx);
  });
  const list = x.kids(tbl);
  list.splice(list.indexOf(source) + 1, 0, row);
  frameHeight(entry.shape, Number(x.attrs(row).h) || 0);
  return { slide: slide.number, rows: rows.length + 1 };
}

function deleteRow(deck, op) {
  const slide = deck.slide(op.slide);
  const entry = tableShapeOf(deck, slide, op.shape);
  const tbl = tableOfShape(entry);
  const rows = x.children(tbl, "a:tr");
  if (rows.length === 1) throw new Error("a table needs one row; delete the table shape instead");
  const tr = rows[index(op.row, rows.length, "row")];
  x.kids(tbl).splice(x.kids(tbl).indexOf(tr), 1);
  frameHeight(entry.shape, -(Number(x.attrs(tr).h) || 0));
  return { slide: slide.number, rows: rows.length - 1 };
}

function updateChart(deck, op) {
  const slide = deck.slide(op.slide);
  let entry;
  if (op.shape !== undefined) entry = findShape(deck, slide, op.shape);
  else {
    const charts = shapeEntries(deck.spTree(slide.path)).filter((item) => chartOf(deck, slide.path, item.shape));
    if (charts.length !== 1) throw new Error(`slide ${slide.number} has ${charts.length} charts; give the chart's shape id`);
    entry = charts[0];
  }
  const current = x.tagOf(entry.shape) === "p:graphicFrame" ? chartOf(deck, slide.path, entry.shape) : undefined;
  if (!current) throw new Error(`shape ${shapeLabel(entry)} is not a chart`);
  const data = S.chartData(op);
  const root = x.rootOf(deck.pkg.xml(current.part));
  const plotArea = x.child(x.child(root, "c:chart"), "c:plotArea");
  const plots = x.kids(plotArea).filter((node) => /Chart$/.test(x.tagOf(node) ?? "") && x.children(node, "c:ser").length);
  if (plots.length !== 1) throw new Error(`the chart on slide ${slide.number} has ${plots.length} plots; only a chart with one plot can take new data`);
  const plot = plots[0];
  if (["c:scatterChart", "c:bubbleChart"].includes(x.tagOf(plot))) throw new Error("a scatter or bubble chart cannot take new data here");
  if (["c:pieChart", "c:pie3DChart", "c:doughnutChart", "c:ofPieChart"].includes(x.tagOf(plot)) && data.series.length !== 1) throw new Error("a pie chart takes one series");
  const sers = x.children(plot, "c:ser");
  const firstVal = x.child(sers[0], "c:val");
  const formatNode = firstVal && x.child(x.child(firstVal, "c:numRef") ?? x.el("c:numRef"), "c:numCache");
  const format = (formatNode && x.textOf(x.child(formatNode, "c:formatCode"))) || "General";
  const list = x.kids(plot);
  while (sers.length < data.series.length) {
    const copy = clone(sers[sers.length - 1]);
    for (const tag of ["c:spPr", "c:dPt", "c:dLbls", "c:trendline", "c:errBars"]) x.removeChildren(copy, tag);
    list.splice(list.indexOf(sers[sers.length - 1]) + 1, 0, copy);
    sers.push(copy);
  }
  while (sers.length > data.series.length) list.splice(list.indexOf(sers.pop()), 1);
  const used = new Set();
  sers.forEach((ser, i) => {
    const refs = S.seriesRefs(data, i, format);
    for (const [tag, node] of [["c:tx", refs.tx], ["c:cat", refs.cat], ["c:val", refs.val]]) {
      x.removeChildren(ser, tag);
      x.insertOrdered(ser, node, SER_ORDER);
    }
    const idx = x.child(ser, "c:idx");
    let n = Number(x.attrs(idx).val);
    while (used.has(n)) n++;
    used.add(n);
    x.attrs(idx).val = String(n);
    x.attrs(x.child(ser, "c:order")).val = String(i);
    const kids = x.kids(ser);
    for (let k = kids.length - 1; k >= 0; k--) {
      if (x.tagOf(kids[k]) === "c:dPt" && Number(x.attrs(x.child(kids[k], "c:idx")).val) >= data.categories.length) kids.splice(k, 1);
    }
    if (data.series[i].color && x.tagOf(plot) !== "c:pieChart") {
      let spPr = x.child(ser, "c:spPr");
      if (!spPr) spPr = x.insertOrdered(ser, x.el("c:spPr"), SER_ORDER);
      if (x.tagOf(plot) === "c:lineChart") {
        let ln = x.child(spPr, "a:ln");
        if (!ln) ln = x.insertOrdered(spPr, x.el("a:ln", { w: 28575 }), ["a:xfrm", "a:custGeom", "a:prstGeom", ...FILLS, "a:ln"]);
        for (const fill of FILLS) x.removeChildren(ln, fill);
        x.kids(ln).unshift(S.solidFill(data.series[i].color, "series color"));
      } else {
        for (const fill of FILLS) x.removeChildren(spPr, fill);
        x.insertOrdered(spPr, S.solidFill(data.series[i].color, "series color"), ["a:xfrm", "a:custGeom", "a:prstGeom", ...FILLS, "a:ln"]);
      }
    }
  });
  const notes = [];
  const external = x.child(root, "c:externalData");
  const rel = external && relsOf(deck.pkg, current.part).find((item) => item.id === x.attrs(external)["r:id"]);
  if (rel?.part && /\.xlsx$/i.test(rel.part)) deck.pkg.files.set(rel.part, S.workbookBytes(data));
  else if (rel) notes.push("the chart's data file is not an .xlsx workbook, so Edit Data in PowerPoint shows the old numbers");
  if (op.title !== undefined) {
    const chart = x.child(root, "c:chart");
    x.removeChildren(chart, "c:title");
    const auto = x.child(chart, "c:autoTitleDeleted");
    if (op.title) {
      const title = S.chartXml({ chart: "column", categories: ["a"], series: [{ values: [1] }], title: op.title }).tree[0];
      const node = x.child(x.child(title, "c:chart"), "c:title");
      x.kids(chart).unshift(node);
      if (auto) x.attrs(auto).val = "0";
    } else if (auto) x.attrs(auto).val = "1";
  }
  return { slide: slide.number, series: data.series.length, categories: data.categories.length, notes: notes.length ? notes : undefined };
}

function replaceImage(deck, op) {
  const slide = deck.slide(op.slide);
  const entry = findShape(deck, slide, op.shape);
  if (x.tagOf(entry.shape) !== "p:pic") throw new Error(`shape ${shapeLabel(entry)} is not a picture`);
  const image = S.loadImage(op.image);
  const media = deck.addMedia(image.bytes, image.kind);
  const rid = deck.relate(slide.path, T.REL_TYPES.image, media);
  const fill = x.child(entry.shape, "p:blipFill");
  const blip = x.child(fill, "a:blip");
  const old = x.attrs(blip)["r:embed"];
  x.attrs(blip)["r:embed"] = rid;
  x.removeChildren(fill, "a:srcRect");
  const ph = B.phOf(entry.shape);
  const box = B.boxOf(entry.shape) ?? (ph && B.resolvedBox(deck, deck.layoutOf(slide.path), ph));
  if (box) {
    const { crop } = S.fitBox(box, image.width, image.height, op.fit ?? "fill");
    if (crop) x.insertOrdered(fill, x.el("a:srcRect", crop), ["a:blip", "a:srcRect", "a:tile", "a:stretch"]);
  }
  if (op.alt !== undefined) x.attrs(B.cNvPrOf(entry.shape)).descr = String(op.alt);
  let stillUsed = false;
  walk(deck.slideRoot(slide.path), (node) => {
    const a = node[":@"];
    if (a && (a["r:embed"] === old || a["r:link"] === old || a["r:id"] === old)) stillUsed = true;
  });
  if (old && !stillUsed) {
    deck.pkg.removeRel(slide.path, old);
    deck.collectGarbage();
  }
  return { slide: slide.number, shape: Number(x.attrs(B.cNvPrOf(entry.shape)).id) };
}

function moveShape(deck, op) {
  const slide = deck.slide(op.slide);
  const entry = findShape(deck, slide, op.shape);
  const ph = B.phOf(entry.shape);
  const current = B.boxOf(entry.shape) ?? (ph && B.resolvedBox(deck, deck.layoutOf(slide.path), ph));
  const box = { ...(current ?? {}) };
  for (const key of ["x", "y", "w", "h"]) if (op[key] !== undefined) box[key] = S.cm(op[key], `moveShape ${key}`);
  for (const key of ["x", "y", "w", "h"]) if (box[key] === undefined) throw new Error("the shape has no position yet; give x, y, w and h");
  const tag = x.tagOf(entry.shape);
  if (tag === "p:graphicFrame") {
    const at = x.kids(entry.shape).findIndex((node) => x.tagOf(node) === "p:xfrm");
    const node = S.xfrmNode("p:xfrm", box);
    if (at >= 0) x.kids(entry.shape)[at] = node;
    else x.kids(entry.shape).splice(1, 0, node);
  } else {
    const holderTag = tag === "p:grpSp" ? "p:grpSpPr" : "p:spPr";
    let holder = x.child(entry.shape, holderTag);
    if (!holder) {
      holder = x.el(holderTag);
      x.kids(entry.shape).splice(1, 0, holder);
    }
    const old = x.child(holder, "a:xfrm");
    const node = S.xfrmNode("a:xfrm", box);
    if (old) {
      for (const extra of x.kids(old)) if (["a:chOff", "a:chExt"].includes(x.tagOf(extra))) x.kids(node).push(extra);
      Object.assign(x.attrs(node), x.attrs(old));
      x.kids(holder)[x.kids(holder).indexOf(old)] = node;
    } else x.kids(holder).unshift(node);
  }
  return { slide: slide.number, shape: Number(x.attrs(B.cNvPrOf(entry.shape)).id) };
}

function deleteShape(deck, op) {
  const slide = deck.slide(op.slide);
  const entry = findShape(deck, slide, op.shape);
  const list = x.kids(entry.container);
  list.splice(list.indexOf(entry.top), 1);
  const root = deck.slideRoot(slide.path);
  const ids = new Set();
  walk(entry.top, (node) => {
    if (x.tagOf(node) === "p:cNvPr") ids.add(x.attrs(node).id);
  });
  let animated = false;
  const timing = x.child(root, "p:timing");
  if (timing) {
    walk(timing, (node) => {
      if (node[":@"]?.spid !== undefined && ids.has(node[":@"].spid)) animated = true;
    });
    if (animated) x.removeChildren(root, "p:timing");
  }
  const used = new Set();
  walk(deck.slideRoot(slide.path), (node) => {
    const a = node[":@"];
    if (a) for (const key of ["r:embed", "r:link", "r:id"]) if (a[key]) used.add(a[key]);
  });
  for (const rel of relsOf(deck.pkg, slide.path)) {
    if ([T.REL_TYPES.image, T.REL_TYPES.chart, T.REL_TYPES.hyperlink].includes(rel.type) && !used.has(rel.id)) deck.pkg.removeRel(slide.path, rel.id);
  }
  deck.collectGarbage();
  return { slide: slide.number, deleted: shapeLabel(entry), notes: animated ? ["the slide's animations were removed, because one of them used the deleted shape"] : undefined };
}

const SLIDE_OPS = new Set(["op", "after"]);

function applyOp(deck, op, i) {
  if (!op || typeof op !== "object" || Array.isArray(op)) throw new Error(`op ${i + 1} must be an object`);
  const run = () => {
    switch (op.op) {
      case "addSlide": {
        const spec = Object.fromEntries(Object.entries(op).filter(([key]) => !SLIDE_OPS.has(key)));
        const count = deck.slides().length;
        const position = op.after !== undefined ? index(op.after, count, "after") + 2 : spec.position;
        delete spec.position;
        const path = B.buildSlide(deck, spec, position);
        return { slide: deck.slides().find((slide) => slide.path === path).number };
      }
      case "duplicate": {
        const slide = deck.slide(op.slide);
        const position = op.position ?? slide.number + 1;
        const path = deck.duplicateSlide(slide.path, position);
        return { slide: deck.slides().find((entry) => entry.path === path).number };
      }
      case "delete": {
        const list = slidesFor(deck, op.slide ?? (() => {
          throw new Error("delete needs slide: a number or a list");
        })());
        for (const slide of [...list].sort((a, b) => b.number - a.number)) deck.removeSlide(deck.slides().find((entry) => entry.path === slide.path));
        return { deleted: list.map((slide) => slide.number), slides: deck.slides().length };
      }
      case "move": {
        const slide = deck.slide(op.slide);
        const count = deck.slides().length;
        const to = index(op.to, count, "to") + 1;
        deck.moveSlide(slide, to);
        return { slide: to };
      }
      case "replace":
        return replaceText(deck, op);
      case "setText":
        return setText(deck, op);
      case "addShape": {
        const slide = deck.slide(op.slide);
        const { op: _op, slide: _slide, ...spec } = op;
        const ctx = B.contextFor(deck, slide.path);
        const node = S.freeShape(spec, ctx);
        x.kids(ctx.tree).push(node);
        return { slide: slide.number, shape: ctx.ids.last };
      }
      case "deleteShape":
        return deleteShape(deck, op);
      case "moveShape":
        return moveShape(deck, op);
      case "image":
        return replaceImage(deck, op);
      case "cell":
        return setCell(deck, op);
      case "addRow":
        return addRow(deck, op);
      case "deleteRow":
        return deleteRow(deck, op);
      case "chart":
        return updateChart(deck, op);
      case "notes": {
        const slide = deck.slide(op.slide);
        const text = op.text ?? "";
        deck.setNotes(slide.path, text === "" ? [] : S.paragraphNodes(text, {}, {}));
        return { slide: slide.number };
      }
      case "background": {
        const list = slidesFor(deck, op.slide);
        for (const slide of list) B.setBackground(deck, slide.path, op.color ?? null);
        return { slides: list.map((slide) => slide.number) };
      }
      case "hide":
      case "show": {
        const list = slidesFor(deck, op.slide);
        for (const slide of list) B.setHidden(deck, slide.path, op.op === "hide");
        return { slides: list.map((slide) => slide.number) };
      }
      case "properties": {
        const { op: _op, ...values } = op;
        deck.setProperties(values);
        return {};
      }
      default:
        throw new Error(`unknown op ${JSON.stringify(op.op)}; use addSlide, duplicate, delete, move, replace, setText, addShape, deleteShape, moveShape, image, cell, addRow, deleteRow, chart, notes, background, hide, show or properties`);
    }
  };
  try {
    return { op: op.op, ...run() };
  } catch (error) {
    throw new Error(`op ${i + 1} (${op.op}): ${error.message}`);
  }
}

module.exports = { applyOp, normalize, setParagraphs };
