"use strict";

const x = require("./ooxml.cjs");
const T = require("./template.cjs");
const { walk } = require("./document.cjs");
const { collectRuns, paragraphText, runProps } = require("./text.cjs");

function numbering(doc) {
  const map = new Map();
  doc.paragraphs().forEach(({ p }, i) => map.set(p, i + 1));
  return map;
}

function tableNumbers(doc) {
  const map = new Map();
  doc.tables().forEach(({ tbl }, i) => map.set(tbl, i + 1));
  return map;
}

function bodyBlocks(node) {
  const out = [];
  for (const entry of x.kids(node)) {
    const tag = x.tagOf(entry);
    if (tag === "w:p" || tag === "w:tbl") out.push(entry);
    else if (tag === "w:sdt") {
      const content = x.child(entry, "w:sdtContent");
      if (content) out.push(...bodyBlocks(content));
    } else if (tag === "w:customXml") out.push(...bodyBlocks(entry));
  }
  return out;
}

function keep(entry, mode) {
  if (mode === "accept") return !entry.deleted;
  if (mode === "reject") return !entry.inserted;
  return true;
}

function pieceDisplay(piece, markup) {
  if (piece.tag === "w:drawing" || piece.tag === "w:pict") {
    let alt = "";
    walk(piece.node, (node, tag) => {
      if (tag === "wp:docPr" && !alt) alt = node[":@"]?.descr || "";
    });
    return markup ? `![${alt || "image"}]` : alt ? `[image: ${alt}]` : "[image]";
  }
  if (piece.tag === "w:footnoteReference") return `[^${x.attrs(piece.node)["w:id"]}]`;
  if (piece.tag === "w:endnoteReference") return `[^e${x.attrs(piece.node)["w:id"]}]`;
  return piece.text;
}

function escapeMarkdown(text) {
  return text.replace(/([\\*_`[\]])/g, "\\$1");
}

function wrapEmphasis(text, marker) {
  if (!marker || !text.trim()) return text;
  const lead = /^\s*/.exec(text)[0];
  const tail = /\s*$/.exec(text)[0];
  return `${lead}${marker}${text.trim()}${marker}${tail}`;
}

function inlineText(doc, p, options) {
  const { format, changes } = options;
  const markup = format === "markdown";
  const runs = collectRuns(p).filter((entry) => keep(entry, changes));
  const groups = [];
  for (const entry of runs) {
    const text = entry.pieces.map((piece) => (markup && ["w:t", "w:delText"].includes(piece.tag) ? escapeMarkdown(piece.text) : pieceDisplay(piece, markup))).join("");
    if (!text) continue;
    const props = runProps(entry.run, changes);
    let link;
    if (entry.link) {
      const a = x.attrs(entry.link);
      link = a["r:id"] ? doc.relTarget(a["r:id"])?.target : a["w:anchor"] ? `#${a["w:anchor"]}` : undefined;
    }
    const change = changes === "show" ? (entry.deleted ? "del" : entry.inserted ? "ins" : "") : "";
    const key = markup ? `${props.bold}|${props.italic}|${link ?? ""}|${change}` : change;
    const last = groups[groups.length - 1];
    if (last && last.key === key) last.text += text;
    else groups.push({ key, text, props, link, change });
  }
  const styled = groups.map((group) => {
    let text = group.text;
    if (markup) {
      text = wrapEmphasis(text, group.props.bold && group.props.italic ? "***" : group.props.bold ? "**" : group.props.italic ? "*" : "");
      if (group.link) text = `[${text}](${group.link})`;
    }
    return { change: group.change, text };
  });
  const merged = [];
  for (const group of styled) {
    const last = merged[merged.length - 1];
    if (last && last.change === group.change) last.text += group.text;
    else merged.push({ ...group });
  }
  return merged.map((group) => (group.change === "ins" ? `{++${group.text}++}` : group.change === "del" ? `{--${group.text}--}` : group.text)).join("");
}

function hiddenParagraph(p, options) {
  if (options.changes === "show") return false;
  const pPr = x.child(p, "w:pPr");
  const mark = pPr && x.child(pPr, "w:rPr");
  if (!mark || !x.child(mark, options.changes === "reject" ? "w:ins" : "w:del")) return false;
  return inlineText(undefined, p, { ...options, format: "text" }) === "";
}

function paragraphLine(doc, p, n, options) {
  const inline = inlineText(doc, p, options);
  const prefix = options.numbers ? `[${n}] ` : "";
  if (options.format !== "markdown") return `${prefix}${inline}`;
  const level = doc.headingLevel(p);
  if (level && inline.trim()) return `${prefix}${"#".repeat(Math.min(level, 6))} ${inline}`;
  const list = doc.listInfo(p);
  if (list) return `${"  ".repeat(list.level)}${list.ordered ? "1." : "-"} ${prefix}${inline}`;
  const style = (doc.styleName(p) ?? "").toLowerCase();
  if (style === "quote" || style === "intense quote") return `> ${prefix}${inline}`;
  return `${prefix}${inline}`;
}

function cellText(doc, tc, nums, options) {
  const parts = [];
  for (const block of bodyBlocks(tc)) {
    if (x.tagOf(block) === "w:p") {
      if (hiddenParagraph(block, options)) continue;
      const text = inlineText(doc, block, options);
      parts.push(options.numbers ? `[${nums.get(block)}] ${text}` : text);
    } else {
      for (const tr of x.children(block, "w:tr")) {
        for (const inner of x.children(tr, "w:tc")) parts.push(cellText(doc, inner, nums, options));
      }
    }
  }
  return parts.filter((part) => part !== "").join(options.format === "markdown" ? "<br>" : " / ");
}

function tableRows(doc, tbl, nums, options) {
  const rows = [];
  for (const tr of x.children(tbl, "w:tr")) {
    const trPr = x.child(tr, "w:trPr");
    if (options.changes === "accept" && trPr && x.child(trPr, "w:del")) continue;
    if (options.changes === "reject" && trPr && x.child(trPr, "w:ins")) continue;
    const row = [];
    for (const tc of x.children(tr, "w:tc")) {
      const tcPr = x.child(tc, "w:tcPr");
      const span = Number(tcPr && x.child(tcPr, "w:gridSpan") ? x.attrs(x.child(tcPr, "w:gridSpan"))["w:val"] : 1) || 1;
      const vMerge = tcPr && x.child(tcPr, "w:vMerge");
      const continued = vMerge && x.attrs(vMerge)["w:val"] !== "restart";
      row.push(continued ? "" : cellText(doc, tc, nums, options));
      for (let i = 1; i < span; i++) row.push("");
    }
    rows.push(row);
  }
  return rows;
}

function renderTable(rows, format) {
  if (!rows.length) return "";
  const width = Math.max(...rows.map((row) => row.length));
  const pad = (row) => [...row, ...Array(width - row.length).fill("")];
  if (format !== "markdown") return rows.map((row) => pad(row).join("\t")).join("\n");
  const cell = (text) => text.replace(/\|/g, "\\|").replace(/\n/g, "<br>");
  const lines = [`| ${pad(rows[0]).map(cell).join(" | ")} |`, `|${Array(width).fill("---").join("|")}|`];
  for (const row of rows.slice(1)) lines.push(`| ${pad(row).map(cell).join(" | ")} |`);
  return lines.join("\n");
}

function readDocument(doc, options) {
  const nums = numbering(doc);
  const tnums = tableNumbers(doc);
  const from = options.from ?? 1;
  const to = options.to ?? Infinity;
  const limit = options.maxChars ?? 60000;
  const blocks = bodyBlocks(doc.body);
  const out = [];
  let size = 0;
  let stoppedAt;
  for (const block of blocks) {
    const first = x.tagOf(block) === "w:p" ? nums.get(block) : nums.get(doc.paragraphs().find(({ p }) => isInside(block, p))?.p);
    if (first !== undefined && (first < from || first > to)) continue;
    if (size > limit) {
      stoppedAt = first;
      break;
    }
    if (x.tagOf(block) === "w:p") {
      if (hiddenParagraph(block, options)) continue;
      const line = paragraphLine(doc, block, nums.get(block), options);
      if (options.format === "json") out.push({ type: "paragraph", n: nums.get(block), style: doc.styleName(block), heading: doc.headingLevel(block) || undefined, list: doc.listInfo(block) ? { level: doc.listInfo(block).level, ordered: doc.listInfo(block).ordered } : undefined, text: line });
      else out.push(line);
      size += line.length;
    } else {
      const rows = tableRows(doc, block, nums, options);
      if (options.format === "json") out.push({ type: "table", table: tnums.get(block), firstParagraph: first, rows });
      else {
        const text = `${options.numbers ? `[table ${tnums.get(block)}]\n` : ""}${renderTable(rows, options.format)}`;
        out.push(text);
        size += text.length;
      }
    }
  }
  if (options.format === "json") {
    const result = { paragraphs: nums.size, blocks: out };
    if (stoppedAt) result.truncated = { next: stoppedAt };
    if (options.comments) result.comments = listComments(doc);
    return JSON.stringify(result, null, 2);
  }
  const listLine = /^\s*(-|1\.) /;
  let text = "";
  out.forEach((line, i) => {
    if (i > 0) text += options.format === "markdown" && !(listLine.test(line) && listLine.test(out[i - 1])) ? "\n\n" : "\n";
    text += line;
  });
  if (stoppedAt) text += `\n\nThe output stops here. Run read again with --from ${stoppedAt} to see the rest.`;
  if (options.comments) {
    const comments = listComments(doc);
    if (comments.length) text += `\n\n---\nComments:\n${comments.map((c) => `- [c${c.id}] ${c.author ?? "unknown"} on "${c.anchor}" (paragraph ${c.paragraph ?? "?"}): ${c.text}`).join("\n")}`;
  }
  return text;
}

function isInside(container, target) {
  let found = false;
  walk(container, (node) => {
    if (node === target) found = true;
  });
  return found;
}

function listComments(doc) {
  const root = doc.commentsRoot(false);
  if (!root) return [];
  const anchors = new Map();
  const where = new Map();
  const nums = numbering(doc);
  const open = new Set();
  for (const { p } of doc.paragraphs()) {
    const visit = (node, deleted) => {
      for (const entry of x.kids(node)) {
        const tag = x.tagOf(entry);
        if (tag === "w:commentRangeStart") {
          const id = x.attrs(entry)["w:id"];
          open.add(id);
          if (!where.has(id)) where.set(id, nums.get(p));
        } else if (tag === "w:commentRangeEnd") open.delete(x.attrs(entry)["w:id"]);
        else if (tag === "w:commentReference") {
          const id = x.attrs(entry)["w:id"];
          if (!where.has(id)) where.set(id, nums.get(p));
        } else if (tag === "w:r") {
          if (!deleted) {
            const text = collectRuns({ "w:p": [entry] }).map((run) => run.text).join("");
            for (const id of open) anchors.set(id, (anchors.get(id) ?? "") + text);
          }
          visit(entry, deleted);
        } else if (["w:ins", "w:moveTo", "w:hyperlink", "w:smartTag", "w:fldSimple", "w:customXml", "w:sdtContent", "w:sdt"].includes(tag)) visit(entry, deleted);
        else if (["w:del", "w:moveFrom"].includes(tag)) visit(entry, true);
      }
    };
    visit(p, false);
  }
  return x.children(root, "w:comment").map((node) => {
    const a = x.attrs(node);
    const text = x.children(node, "w:p").map((p) => paragraphText(p)).join("\n");
    return { id: a["w:id"], author: a["w:author"], initials: a["w:initials"], date: a["w:date"], anchor: anchors.get(a["w:id"]) ?? "", paragraph: where.get(a["w:id"]), text };
  });
}

function listChanges(doc) {
  const nums = numbering(doc);
  const out = [];
  const add = (node, type, n, text) => {
    const a = x.attrs(node);
    out.push({ id: a["w:id"], type, author: a["w:author"], date: a["w:date"], paragraph: n, text });
  };
  for (const { p } of doc.paragraphs()) {
    const n = nums.get(p);
    const pPr = x.child(p, "w:pPr");
    const markRPr = pPr && x.child(pPr, "w:rPr");
    if (markRPr && x.child(markRPr, "w:ins")) add(x.child(markRPr, "w:ins"), "paragraph inserted", n);
    if (markRPr && x.child(markRPr, "w:del")) add(x.child(markRPr, "w:del"), "paragraph deleted", n);
    if (pPr && x.child(pPr, "w:pPrChange")) add(x.child(pPr, "w:pPrChange"), "paragraph format", n);
    const visit = (node) => {
      for (const entry of x.kids(node)) {
        const tag = x.tagOf(entry);
        if (tag === "w:ins" || tag === "w:moveTo" || tag === "w:del" || tag === "w:moveFrom") {
          const text = collectRuns({ "w:p": x.kids(entry) }).map((run) => run.text).join("");
          add(entry, { "w:ins": "insert", "w:moveTo": "move to", "w:del": "delete", "w:moveFrom": "move from" }[tag], n, text);
          visit(entry);
        } else if (tag === "w:r") {
          const rPr = x.child(entry, "w:rPr");
          if (rPr && x.child(rPr, "w:rPrChange")) add(x.child(rPr, "w:rPrChange"), "format", n, collectRuns({ "w:p": [entry] }).map((run) => run.text).join(""));
        } else if (["w:hyperlink", "w:smartTag", "w:fldSimple", "w:customXml", "w:sdt", "w:sdtContent"].includes(tag)) visit(entry);
      }
    };
    visit(p);
  }
  for (const { tbl } of doc.tables()) {
    const first = doc.paragraphs().find(({ p }) => isInside(tbl, p));
    for (const tr of x.children(tbl, "w:tr")) {
      const trPr = x.child(tr, "w:trPr");
      if (trPr && x.child(trPr, "w:ins")) add(x.child(trPr, "w:ins"), "row inserted", first ? nums.get(first.p) : undefined);
      if (trPr && x.child(trPr, "w:del")) add(x.child(trPr, "w:del"), "row deleted", first ? nums.get(first.p) : undefined);
    }
  }
  return out;
}

function sections(doc) {
  const list = [];
  for (const { p } of doc.paragraphs()) {
    const pPr = x.child(p, "w:pPr");
    const sect = pPr && x.child(pPr, "w:sectPr");
    if (sect) list.push(sect);
  }
  if (doc.sectPr()) list.push(doc.sectPr());
  return list.map((sect) => {
    const size = x.child(sect, "w:pgSz");
    const a = size ? x.attrs(size) : {};
    const w = Number(a["w:w"]);
    const h = Number(a["w:h"]);
    const named = Object.entries(T.PAGE_SIZES).find(([, dims]) => (dims[0] === w && dims[1] === h) || (dims[0] === h && dims[1] === w));
    return {
      page: named ? named[0].toUpperCase().replace("LETTER", "Letter").replace("LEGAL", "Legal") : w && h ? `${(w / 567).toFixed(1)} x ${(h / 567).toFixed(1)} cm` : "unknown",
      orientation: a["w:orient"] === "landscape" || w > h ? "landscape" : "portrait",
      headers: x.children(sect, "w:headerReference").length,
      footers: x.children(sect, "w:footerReference").length,
    };
  });
}

function info(doc) {
  const paragraphs = doc.paragraphs();
  let words = 0;
  let characters = 0;
  const headings = [];
  let images = 0;
  let fields = 0;
  for (const [i, { p }] of paragraphs.entries()) {
    const text = paragraphText(p);
    characters += text.length;
    words += text.split(/\s+/).filter(Boolean).length;
    const level = doc.headingLevel(p);
    if (level && text.trim() && headings.length < 200) headings.push({ n: i + 1, level, text: text.trim() });
  }
  walk(doc.root, (node, tag) => {
    if (tag === "w:drawing" || tag === "w:pict") images++;
    if (tag === "w:fldSimple" || (tag === "w:fldChar" && node[":@"]?.["w:fldCharType"] === "begin")) fields++;
  });
  const changes = listChanges(doc);
  const authors = {};
  for (const change of changes) authors[change.author ?? "unknown"] = (authors[change.author ?? "unknown"] ?? 0) + 1;
  const nums = numbering(doc);
  return {
    properties: doc.properties(),
    paragraphs: paragraphs.length,
    words,
    characters,
    headings,
    tables: doc.tables().map(({ tbl }, i) => {
      const rows = x.children(tbl, "w:tr");
      const first = paragraphs.find(({ p }) => isInside(tbl, p));
      return { table: i + 1, rows: rows.length, columns: Math.max(0, ...rows.map((tr) => x.children(tr, "w:tc").length)), firstParagraph: first ? nums.get(first.p) : undefined, firstCell: first ? paragraphText(first.p).slice(0, 60) : "" };
    }),
    images,
    fields,
    sections: sections(doc),
    comments: listComments(doc).length,
    trackedChanges: { total: changes.length, byAuthor: authors, trackingOn: doc.setting("w:trackRevisions") },
  };
}

module.exports = { info, listChanges, listComments, readDocument };
