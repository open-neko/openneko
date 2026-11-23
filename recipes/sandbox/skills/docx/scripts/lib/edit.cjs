"use strict";

const x = require("./ooxml.cjs");
const B = require("./build.cjs");
const { clone, paragraphsIn } = require("./document.cjs");
const { findAll, isolate, model, normalize, paragraphText, textElement } = require("./text.cjs");
const { resolve } = require("./revisions.cjs");

const CONTAINERS = ["w:ins", "w:moveTo", "w:hyperlink", "w:smartTag", "w:fldSimple", "w:customXml"];

function toDeleted(node) {
  if (!node || typeof node !== "object" || "#text" in node) return;
  let tag = x.tagOf(node);
  const map = { "w:t": "w:delText", "w:instrText": "w:delInstrText" };
  if (map[tag]) {
    node[map[tag]] = node[tag];
    delete node[tag];
    tag = map[tag];
  }
  if (Array.isArray(node[tag]) && tag !== "w:rPr") node[tag].forEach(toDeleted);
}

function deleteContent(container, ctx) {
  const list = x.kids(container);
  let i = 0;
  while (i < list.length) {
    const tag = x.tagOf(list[i]);
    if (tag === "w:r") {
      let j = i;
      while (j < list.length && x.tagOf(list[j]) === "w:r") j++;
      const group = list.slice(i, j);
      group.forEach(toDeleted);
      list.splice(i, j - i, x.el("w:del", B.stamp(ctx.track), group));
    } else if (CONTAINERS.includes(tag)) deleteContent(list[i], ctx);
    else if (tag === "w:sdt") {
      const content = x.child(list[i], "w:sdtContent");
      if (content) deleteContent(content, ctx);
    }
    i++;
  }
}

function markParagraph(p, kind, ctx) {
  let pPr = x.child(p, "w:pPr");
  if (!pPr) {
    pPr = x.el("w:pPr");
    x.kids(p).unshift(pPr);
  }
  let rPr = x.child(pPr, "w:rPr");
  if (!rPr) rPr = x.insertOrdered(pPr, x.el("w:rPr"), B.PPR_ORDER);
  x.insertOrdered(rPr, x.el(kind, B.stamp(ctx.track)), B.RPR_ORDER);
}

function describe(p, n) {
  return `${n}: "${paragraphText(p).slice(0, 70)}"`;
}

function locate(doc, anchor, what = "paragraph") {
  const all = doc.paragraphs();
  if (typeof anchor === "number") anchor = { paragraph: anchor };
  if (typeof anchor === "string") anchor = { find: anchor };
  if (!anchor || typeof anchor !== "object") throw new Error(`${what} must be a paragraph number, a piece of text, or { paragraph | find | heading | table }`);
  if (anchor.table !== undefined) {
    const tables = doc.tables();
    const n = Number(anchor.table);
    if (!(n >= 1 && n <= tables.length)) throw new Error(`there is no table ${anchor.table}; the document has ${tables.length}`);
    return { node: tables[n - 1].tbl, list: tables[n - 1].list };
  }
  if (anchor.paragraph !== undefined) {
    const n = Number(anchor.paragraph);
    if (!(n >= 1 && n <= all.length)) throw new Error(`there is no paragraph ${anchor.paragraph}; the document has ${all.length}`);
    return { node: all[n - 1].p, list: all[n - 1].list, n };
  }
  const wanted = anchor.find ?? anchor.heading;
  if (wanted === undefined) throw new Error(`${what} needs paragraph, find, heading or table`);
  const hits = [];
  all.forEach((entry, i) => {
    if (anchor.heading !== undefined && !doc.headingLevel(entry.p)) return;
    const text = normalize(paragraphText(entry.p));
    const ok = anchor.heading !== undefined ? text.trim().toLowerCase() === normalize(String(wanted)).trim().toLowerCase() || text.includes(normalize(String(wanted))) : text.includes(normalize(String(wanted)));
    if (ok) hits.push({ node: entry.p, list: entry.list, n: i + 1 });
  });
  if (!hits.length) throw new Error(`no paragraph contains "${wanted}"`);
  if (anchor.occurrence !== undefined) {
    const hit = hits[Number(anchor.occurrence) - 1];
    if (!hit) throw new Error(`"${wanted}" is in ${hits.length} paragraphs; occurrence ${anchor.occurrence} does not exist`);
    return hit;
  }
  if (hits.length > 1) throw new Error(`"${wanted}" is in ${hits.length} paragraphs (${hits.slice(0, 5).map((hit) => describe(hit.node, hit.n)).join("; ")}); use more text, a paragraph number or occurrence`);
  return hits[0];
}

function matchesIn(doc, find, scope) {
  if (typeof find !== "string" || !find) throw new Error("find must be a non-empty string");
  const out = [];
  for (const root of doc.storyRoots(scope)) {
    for (const { p } of paragraphsIn(root.node)) {
      for (const at of findAll(model(p).text, find)) out.push({ p, at, part: root.part });
    }
  }
  return out;
}

function pickMatches(doc, op) {
  const matches = matchesIn(doc, op.find, op.scope);
  if (!matches.length) throw new Error(`"${op.find}" is not in the document${op.scope === "all" ? "" : " body (add \"scope\": \"all\" to search headers, footers and notes)"}`);
  if (op.all) return matches;
  if (op.occurrence !== undefined) {
    const hit = matches[Number(op.occurrence) - 1];
    if (!hit) throw new Error(`"${op.find}" occurs ${matches.length} times; occurrence ${op.occurrence} does not exist`);
    return [hit];
  }
  if (matches.length > 1) {
    const nums = new Map(doc.paragraphs().map(({ p }, i) => [p, i + 1]));
    const where = [...new Set(matches.map((m) => nums.get(m.p) ?? m.part))].slice(0, 8).join(", ");
    throw new Error(`"${op.find}" occurs ${matches.length} times (paragraphs ${where}); add "all": true, "occurrence": N, or use more text`);
  }
  return matches;
}

function baseRPr(run) {
  const rPr = x.child(run, "w:rPr");
  if (!rPr) return undefined;
  const copy = clone(rPr);
  x.removeChildren(copy, "w:rPrChange");
  return copy;
}

function sameAuthor(insNode, ctx) {
  return insNode && String(x.attrs(insNode)["w:author"] ?? "").toLowerCase() === String(ctx.track.author).toLowerCase();
}

function replaceRange(p, from, to, text, ctx) {
  const iso = isolate(p, from, to);
  if (!iso) return;
  const { list, first, last } = iso;
  const rPr = baseRPr(iso.runs[0].run);
  const replacement = text ? B.textRun(text, rPr) : undefined;
  const direct = !ctx.track || (iso.inserted && sameAuthor(iso.insNode, ctx));
  if (ctx.track && iso.inserted && !direct) throw new Error("that text is another author's tracked insertion; accept or reject it first");
  if (direct) {
    for (let i = last; i >= first; i--) if (x.tagOf(list[i]) === "w:r") list.splice(i, 1);
    if (replacement) list.splice(first, 0, replacement);
    return;
  }
  let i = first;
  let end = last;
  let lastDel = -1;
  while (i <= end) {
    if (x.tagOf(list[i]) !== "w:r") {
      i++;
      continue;
    }
    let j = i;
    while (j <= end && x.tagOf(list[j]) === "w:r") j++;
    const group = list.slice(i, j);
    group.forEach(toDeleted);
    list.splice(i, j - i, x.el("w:del", B.stamp(ctx.track), group));
    end -= j - i - 1;
    lastDel = i;
    i++;
  }
  if (replacement) list.splice(lastDel + 1, 0, x.el("w:ins", B.stamp(ctx.track), [replacement]));
}

function replace(doc, op, ctx) {
  if (op.with === undefined) throw new Error("replace needs with (use \"\" to delete the text)");
  const matches = pickMatches(doc, op);
  const byParagraph = new Map();
  for (const m of matches) {
    if (!byParagraph.has(m.p)) byParagraph.set(m.p, []);
    byParagraph.get(m.p).push(m.at);
  }
  const length = op.find.length;
  for (const [p, offsets] of byParagraph) {
    for (const at of offsets.sort((a, b) => b - a)) replaceRange(p, at, at + length, String(op.with), ctx);
  }
  return { replaced: matches.length };
}

function blocksOf(op) {
  if (op.blocks !== undefined) return op.blocks;
  if (op.markdown !== undefined) return [{ markdown: op.markdown }];
  if (op.text !== undefined) return [{ paragraph: op.text }];
  throw new Error("insert needs blocks, markdown or text");
}

function insert(doc, op, ctx) {
  const nodes = B.blockNodes(blocksOf(op), ctx);
  if (ctx.track) B.markInserted(nodes, ctx.track);
  if (op.after !== undefined || op.before !== undefined) {
    const target = locate(doc, op.after ?? op.before, op.after !== undefined ? "after" : "before");
    const at = target.list.indexOf(target.node);
    target.list.splice(op.after !== undefined ? at + 1 : at, 0, ...nodes);
  } else if (op.at === "start") x.kids(doc.body).unshift(...nodes);
  else if (op.at === "end" || op.at === undefined) {
    const list = x.kids(doc.body);
    const sect = list.findIndex((node) => x.tagOf(node) === "w:sectPr");
    list.splice(sect < 0 ? list.length : sect, 0, ...nodes);
  } else throw new Error('insert needs after, before, or at ("start" or "end")');
  return { inserted: nodes.length };
}

function removeParagraph(p, list) {
  const pPr = x.child(p, "w:pPr");
  if (pPr && x.child(pPr, "w:sectPr")) throw new Error("that paragraph ends a section; delete its text with replace instead");
  const blocks = list.filter((node) => x.tagOf(node) === "w:p" || x.tagOf(node) === "w:tbl");
  const at = list.indexOf(p);
  if (blocks.length === 1 && blocks[0] === p) list.splice(at, 1, x.el("w:p", null, pPr ? [clone(pPr)] : []));
  else list.splice(at, 1);
}

function deleteTable(tbl, list, ctx) {
  if (!ctx.track) {
    list.splice(list.indexOf(tbl), 1);
    return;
  }
  for (const tr of x.children(tbl, "w:tr")) deleteRowTracked(tr, ctx);
}

function deleteRowTracked(tr, ctx) {
  let trPr = x.child(tr, "w:trPr");
  if (!trPr) {
    trPr = x.el("w:trPr");
    const list = x.kids(tr);
    const at = list.findIndex((entry) => x.tagOf(entry) === "w:tc");
    list.splice(at < 0 ? list.length : at, 0, trPr);
  }
  x.insertOrdered(trPr, x.el("w:del", B.stamp(ctx.track)), B.TRPR_ORDER);
  for (const tc of x.children(tr, "w:tc")) for (const { p } of paragraphsIn(tc)) deleteContent(p, ctx);
}

function del(doc, op, ctx) {
  const anchor = op.table !== undefined ? { table: op.table } : op.paragraph !== undefined ? { paragraph: op.paragraph } : op.heading !== undefined ? { heading: op.heading, occurrence: op.occurrence } : { find: op.find, occurrence: op.occurrence };
  const target = locate(doc, anchor, "delete");
  if (x.tagOf(target.node) === "w:tbl") {
    deleteTable(target.node, target.list, ctx);
    return { deleted: 1 };
  }
  const count = Number(op.count ?? 1);
  if (!(count >= 1)) throw new Error("count must be 1 or more");
  const start = target.list.indexOf(target.node);
  const paragraphs = target.list.slice(start).filter((node) => x.tagOf(node) === "w:p").slice(0, count);
  const lastIndex = target.list.indexOf(paragraphs[paragraphs.length - 1]);
  if (paragraphs.length < count || target.list.slice(start, lastIndex + 1).some((node) => x.tagOf(node) === "w:tbl")) {
    throw new Error(`there are not ${count} paragraphs in a row from paragraph ${target.n ?? "?"} without a table between them; delete the table on its own with "table"`);
  }
  for (const p of paragraphs.reverse()) {
    if (ctx.track) {
      deleteContent(p, ctx);
      markParagraph(p, "w:del", ctx);
    } else removeParagraph(p, target.list);
  }
  return { deleted: count };
}

const FORMAT_KEYS = ["bold", "italic", "underline", "strike", "color", "highlight", "size", "font"];
const FORMAT_TAGS = { bold: ["w:b", "w:bCs"], italic: ["w:i", "w:iCs"], underline: ["w:u"], strike: ["w:strike"], color: ["w:color"], highlight: ["w:highlight"], size: ["w:sz", "w:szCs"], font: ["w:rFonts"] };

function setFormat(run, props, ctx) {
  let rPr = x.child(run, "w:rPr");
  if (!rPr) {
    rPr = x.el("w:rPr");
    x.kids(run).unshift(rPr);
  }
  if (ctx.track && !x.child(rPr, "w:rPrChange")) {
    const old = x.el("w:rPr", null, x.kids(clone(rPr)));
    x.insertOrdered(rPr, x.el("w:rPrChange", B.stamp(ctx.track), [old]), B.RPR_ORDER);
  }
  for (const key of FORMAT_KEYS) {
    if (props[key] === undefined) continue;
    for (const tag of FORMAT_TAGS[key]) x.removeChildren(rPr, tag);
    if (props[key] === false || props[key] === null) continue;
    const built = B.rPrFor({ [key]: props[key] }, ctx);
    for (const node of x.kids(built)) x.insertOrdered(rPr, node, B.RPR_ORDER);
  }
}

function format(doc, op, ctx) {
  if (!FORMAT_KEYS.some((key) => op[key] !== undefined)) throw new Error(`format needs one of ${FORMAT_KEYS.join(", ")}`);
  const matches = pickMatches(doc, op);
  for (const m of matches.sort((a, b) => b.at - a.at)) {
    const iso = isolate(m.p, m.at, m.at + op.find.length);
    if (iso) for (const entry of iso.runs) setFormat(entry.run, op, ctx);
  }
  return { formatted: matches.length };
}

function style(doc, op, ctx) {
  const target = locate(doc, op.paragraph !== undefined ? { paragraph: op.paragraph } : { find: op.find, occurrence: op.occurrence }, "style");
  if (x.tagOf(target.node) !== "w:p") throw new Error("style works on a paragraph");
  const styleId = op.heading !== undefined ? doc.headingStyle(Number(op.heading)) : op.style !== undefined ? doc.paragraphStyle(op.style) : undefined;
  if (!styleId && op.align === undefined) throw new Error("style needs style, heading or align");
  const p = target.node;
  let pPr = x.child(p, "w:pPr");
  if (!pPr) {
    pPr = x.el("w:pPr");
    x.kids(p).unshift(pPr);
  }
  if (ctx.track && !x.child(pPr, "w:pPrChange")) {
    const base = x.kids(clone(pPr)).filter((node) => !["w:rPr", "w:sectPr", "w:pPrChange"].includes(x.tagOf(node)));
    x.insertOrdered(pPr, x.el("w:pPrChange", B.stamp(ctx.track), [x.el("w:pPr", null, base)]), B.PPR_ORDER);
  }
  if (styleId) {
    x.removeChildren(pPr, "w:pStyle");
    x.insertOrdered(pPr, x.el("w:pStyle", { "w:val": styleId }), B.PPR_ORDER);
  }
  if (op.align !== undefined) {
    x.removeChildren(pPr, "w:jc");
    const built = B.pPrFor({ align: op.align }, ctx);
    x.insertOrdered(pPr, x.child(built, "w:jc"), B.PPR_ORDER);
  }
  return { paragraph: target.n };
}

function comment(doc, op, ctx) {
  if (!op.text) throw new Error("comment needs text");
  const [m] = pickMatches(doc, { find: op.find, occurrence: op.occurrence });
  if (m.part !== doc.mainPath) throw new Error("comments go on body text only");
  const iso = isolate(m.p, m.at, m.at + op.find.length);
  const id = doc.nextId();
  const author = op.author ?? ctx.author;
  const root = doc.commentsRoot(true);
  const initials = op.initials ?? author.split(/\s+/).map((word) => word[0] ?? "").join("").toUpperCase().slice(0, 4);
  const body = String(op.text)
    .split(/\n/)
    .map((line) => x.el("w:p", null, [B.textRun(line)]));
  x.kids(root).push(x.el("w:comment", { "w:id": id, "w:author": author, "w:date": ctx.date, "w:initials": initials }, body));
  const { list, first, last } = iso;
  const refStyle = doc.findStyle("annotation reference", "character")?.id;
  const ref = x.el("w:r", null, [...(refStyle ? [x.el("w:rPr", null, [x.el("w:rStyle", { "w:val": refStyle })])] : []), x.el("w:commentReference", { "w:id": id })]);
  list.splice(last + 1, 0, x.el("w:commentRangeEnd", { "w:id": id }), ref);
  list.splice(first, 0, x.el("w:commentRangeStart", { "w:id": id }));
  return { comment: id };
}

function tableAt(doc, n) {
  const tables = doc.tables();
  const index = Number(n);
  if (!(index >= 1 && index <= tables.length)) throw new Error(`there is no table ${n}; the document has ${tables.length}`);
  return tables[index - 1].tbl;
}

function rowAt(tbl, n) {
  const rows = x.children(tbl, "w:tr");
  const index = Number(n);
  if (!(index >= 1 && index <= rows.length)) throw new Error(`there is no row ${n}; the table has ${rows.length}`);
  return rows[index - 1];
}

function cellAt(tr, n) {
  const cells = x.children(tr, "w:tc");
  const index = Number(n);
  if (!(index >= 1 && index <= cells.length)) throw new Error(`there is no column ${n}; the row has ${cells.length} cells`);
  return cells[index - 1];
}

function fillCell(tc, content, ctx, fresh) {
  const paragraphs = x.children(tc, "w:p");
  const first = paragraphs[0];
  const pPr = first && x.child(first, "w:pPr");
  const firstRun = first && model(first).runs.find((entry) => !entry.deleted && entry.text);
  const rPr = firstRun ? baseRPr(firstRun.run) : undefined;
  const text = content === undefined || content === null ? "" : String(content);
  if (!ctx.track || fresh) {
    const list = x.kids(tc);
    for (let i = list.length - 1; i >= 0; i--) if (["w:p", "w:tbl", "w:sdt"].includes(x.tagOf(list[i]))) list.splice(i, 1);
    const p = x.el("w:p", null, pPr ? [clone(pPr)] : []);
    const ppr = x.child(p, "w:pPr");
    if (ppr) {
      const mark = x.child(ppr, "w:rPr");
      if (mark) {
        x.removeChildren(mark, "w:ins");
        x.removeChildren(mark, "w:del");
      }
    }
    if (text) x.kids(p).push(B.textRun(text, rPr));
    if (fresh && ctx.track) B.markInserted([p], ctx.track);
    list.push(p);
    return;
  }
  paragraphs.forEach((p, i) => {
    deleteContent(p, ctx);
    if (i > 0) markParagraph(p, "w:del", ctx);
  });
  if (!first) {
    const p = x.el("w:p", null, text ? [B.textRun(text, rPr)] : []);
    B.markInserted([p], ctx.track);
    x.kids(tc).push(p);
  } else if (text) x.kids(first).push(x.el("w:ins", B.stamp(ctx.track), [B.textRun(text, rPr)]));
}

function cell(doc, op, ctx) {
  const tc = cellAt(rowAt(tableAt(doc, op.table), op.row), op.column);
  fillCell(tc, op.text, ctx, false);
  return { table: Number(op.table), row: Number(op.row), column: Number(op.column) };
}

function addRow(doc, op, ctx) {
  const tbl = tableAt(doc, op.table);
  const rows = x.children(tbl, "w:tr");
  const after = op.after === undefined ? rows.length : Number(op.after);
  if (!(after >= 0 && after <= rows.length)) throw new Error(`after must be 0 to ${rows.length}`);
  const row = clone(rows[Math.max(after, 1) - 1]);
  const trPr = x.child(row, "w:trPr");
  if (trPr) for (const tag of ["w:ins", "w:del", "w:trPrChange", "w:tblHeader"]) x.removeChildren(trPr, tag);
  const cells = Array.isArray(op.cells) ? op.cells : [];
  const tcs = x.children(row, "w:tc");
  if (cells.length > tcs.length) throw new Error(`the row has ${tcs.length} cells; cells has ${cells.length}`);
  tcs.forEach((tc, i) => fillCell(tc, cells[i], ctx, true));
  if (ctx.track) {
    let pr = x.child(row, "w:trPr");
    if (!pr) {
      pr = x.el("w:trPr");
      const list = x.kids(row);
      const at = list.findIndex((entry) => x.tagOf(entry) === "w:tc");
      list.splice(at < 0 ? list.length : at, 0, pr);
    }
    x.insertOrdered(pr, x.el("w:ins", B.stamp(ctx.track)), B.TRPR_ORDER);
  }
  const list = x.kids(tbl);
  const anchor = after === 0 ? rows[0] : rows[after - 1];
  list.splice(list.indexOf(anchor) + (after === 0 ? 0 : 1), 0, row);
  return { row: after + 1 };
}

function deleteRow(doc, op, ctx) {
  const tbl = tableAt(doc, op.table);
  const tr = rowAt(tbl, op.row);
  if (ctx.track) deleteRowTracked(tr, ctx);
  else {
    if (x.children(tbl, "w:tr").length === 1) throw new Error("that is the only row; delete the table instead");
    const list = x.kids(tbl);
    list.splice(list.indexOf(tr), 1);
  }
  return { row: Number(op.row) };
}

const OPS = {
  replace,
  insert,
  delete: del,
  format,
  style,
  comment,
  cell,
  addRow,
  deleteRow,
  accept: (doc, op) => ({ accepted: resolve(doc, true, op) }),
  reject: (doc, op) => ({ rejected: resolve(doc, false, op) }),
  properties: (doc, op) => {
    const { op: _op, ...values } = op;
    doc.setProperties(values);
    return {};
  },
  trackChanges: (doc, op) => {
    doc.setSetting("w:trackRevisions", op.on !== false);
    return { trackChanges: op.on !== false };
  },
};

function applyOp(doc, op, index, ctx) {
  const name = op?.op;
  try {
    const fn = OPS[name];
    if (!fn) throw new Error(`unknown op; use ${Object.keys(OPS).join(", ")}`);
    const tracked = op.track ?? Boolean(ctx.track);
    const local = op.track === undefined && op.author === undefined ? ctx : { ...ctx, track: tracked ? ctx.trackFor(op.author ?? ctx.track?.author) : null };
    return fn(doc, op, local);
  } catch (error) {
    throw new Error(`op ${index + 1} (${name ?? "?"}): ${error.message}`);
  }
}

module.exports = { OPS, applyOp, locate, textElement };
