"use strict";

const x = require("./ooxml.cjs");
const { clone } = require("./document.cjs");

const INSERTED = new Set(["w:ins", "w:moveTo"]);
const DELETED = new Set(["w:del", "w:moveFrom"]);
const PASS_THROUGH = new Set(["w:smartTag", "w:customXml", "w:bdo", "w:dir", "w:fldSimple"]);
const NON_TEXT = new Set(["w:drawing", "w:pict", "w:object", "w:fldChar", "w:instrText", "w:delInstrText", "w:footnoteReference", "w:endnoteReference", "w:commentReference", "w:ruby", "w:embeddedObject"]);

function pieceText(node, tag) {
  switch (tag) {
    case "w:t":
    case "w:delText":
      return x.textOf(node);
    case "w:tab":
    case "w:ptab":
      return "\t";
    case "w:br":
    case "w:cr":
      return "\n";
    case "w:noBreakHyphen":
      return "-";
    case "w:sym": {
      const code = parseInt(x.attrs(node)["w:char"] ?? "", 16);
      if (!Number.isFinite(code)) return "";
      return String.fromCharCode(code >= 0xf000 ? code - 0xf000 : code);
    }
    default:
      return "";
  }
}

function collectRuns(p) {
  const runs = [];
  const visit = (parent, state) => {
    const list = x.kids(parent);
    for (const node of list) {
      const tag = x.tagOf(node);
      if (tag === "w:r") runs.push({ run: node, list, ...state });
      else if (INSERTED.has(tag)) visit(node, { ...state, inserted: true, insNode: node });
      else if (DELETED.has(tag)) visit(node, { ...state, deleted: true, delNode: node });
      else if (tag === "w:hyperlink") visit(node, { ...state, link: node });
      else if (PASS_THROUGH.has(tag)) visit(node, state);
      else if (tag === "w:sdt") {
        const content = x.child(node, "w:sdtContent");
        if (content) visit(content, state);
      }
    }
  };
  visit(p, { inserted: false, deleted: false });
  const fields = [];
  for (const entry of runs) {
    entry.pieces = [];
    entry.special = false;
    for (const node of x.kids(entry.run)) {
      const tag = x.tagOf(node);
      if (!tag || tag === "w:rPr") continue;
      if (tag === "w:fldChar") {
        const type = x.attrs(node)["w:fldCharType"];
        if (type === "begin") fields.push("instr");
        else if (type === "separate" && fields.length) fields[fields.length - 1] = "result";
        else if (type === "end") fields.pop();
      }
      if (NON_TEXT.has(tag)) entry.special = true;
      const hidden = fields.length > 0 && fields[fields.length - 1] === "instr";
      entry.pieces.push({ node, tag, text: hidden ? "" : pieceText(node, tag) });
    }
    entry.text = entry.pieces.map((piece) => piece.text).join("");
    entry.inField = fields.length > 0;
  }
  return runs;
}

function runProps(run, mode) {
  let rPr = x.child(run, "w:rPr");
  const change = rPr && x.child(rPr, "w:rPrChange");
  if (mode === "reject" && change) rPr = x.child(change, "w:rPr");
  const on = (name) => {
    const node = rPr && x.child(rPr, name);
    if (!node) return false;
    const value = x.attrs(node)["w:val"];
    return value === undefined || !["0", "false", "none"].includes(value);
  };
  return { bold: on("w:b"), italic: on("w:i"), strike: on("w:strike"), underline: on("w:u") };
}

const NORMAL = { "‘": "'", "’": "'", "‚": "'", "‛": "'", "“": '"', "”": '"', "„": '"', " ": " ", "‑": "-", " ": " " };

function normalize(text) {
  return text.replace(/[‘’‚‛“”„ ‑ ]/g, (ch) => NORMAL[ch]);
}

function model(p) {
  const runs = collectRuns(p);
  let at = 0;
  for (const entry of runs) {
    entry.start = at;
    if (!entry.deleted) at += entry.text.length;
    entry.end = at;
  }
  return { runs, text: runs.filter((entry) => !entry.deleted).map((entry) => entry.text).join("") };
}

function paragraphText(p, mode = "current") {
  return collectRuns(p)
    .filter((entry) => (mode === "current" ? !entry.deleted : mode === "original" ? !entry.inserted : true))
    .map((entry) => entry.text)
    .join("");
}

function findAll(haystack, needle) {
  const out = [];
  if (!needle) return out;
  const hay = normalize(haystack);
  const want = normalize(needle);
  let from = 0;
  for (;;) {
    const at = hay.indexOf(want, from);
    if (at < 0) return out;
    out.push(at);
    from = at + want.length;
  }
}

function emptyRunLike(run) {
  const node = { "w:r": [] };
  if (run[":@"]) node[":@"] = { ...run[":@"] };
  const rPr = x.child(run, "w:rPr");
  if (rPr) node["w:r"].push(clone(rPr));
  return node;
}

function textElement(tag, text) {
  const node = x.el(tag, null, [x.textNode(text)]);
  if (/^\s|\s$/.test(text) || text.includes("  ")) x.attrs(node)["xml:space"] = "preserve";
  return node;
}

function splitRun(entry, offset) {
  if (offset <= 0 || offset >= entry.text.length) return;
  const left = emptyRunLike(entry.run);
  const right = emptyRunLike(entry.run);
  let at = 0;
  for (const piece of entry.pieces) {
    const end = at + piece.text.length;
    if (end <= offset) left["w:r"].push(piece.node);
    else if (at >= offset) right["w:r"].push(piece.node);
    else {
      const cut = offset - at;
      left["w:r"].push(textElement(piece.tag, piece.text.slice(0, cut)));
      right["w:r"].push(textElement(piece.tag, piece.text.slice(cut)));
    }
    at = end;
  }
  const index = entry.list.indexOf(entry.run);
  entry.list.splice(index, 1, left, right);
}

function splitAt(p, offset) {
  const { runs } = model(p);
  const entry = runs.find((run) => !run.deleted && run.start < offset && offset < run.end);
  if (entry) splitRun(entry, offset - entry.start);
}

function isolate(p, from, to) {
  splitAt(p, to);
  splitAt(p, from);
  const { runs } = model(p);
  const inside = runs.filter((run) => !run.deleted && run.start >= from && run.end <= to && run.text.length > 0);
  if (!inside.length) return undefined;
  const list = inside[0].list;
  if (inside.some((run) => run.list !== list)) throw new Error("the text crosses a link, field or tracked change; pick a shorter piece of text");
  const first = list.indexOf(inside[0].run);
  const last = list.indexOf(inside[inside.length - 1].run);
  const between = runs.filter((run) => run.list === list && list.indexOf(run.run) >= first && list.indexOf(run.run) <= last);
  if (between.some((run) => run.special)) throw new Error("the text holds an image, field, note or comment mark; pick text without one");
  if (inside.some((run) => run.inField)) throw new Error("the text is inside a field (for example a page number or a table of contents); edit the field in Word");
  return { list, first, last, runs: inside, inserted: inside[0].inserted, insNode: inside[0].insNode };
}

module.exports = { collectRuns, emptyRunLike, findAll, isolate, model, normalize, paragraphText, runProps, splitAt, textElement };
