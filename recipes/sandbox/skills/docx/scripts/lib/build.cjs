"use strict";

const fs = require("node:fs");
const x = require("./ooxml.cjs");
const T = require("./template.cjs");
const { textElement } = require("./text.cjs");

const RPR_ORDER = ["w:ins", "w:del", "w:moveFrom", "w:moveTo", "w:rStyle", "w:rFonts", "w:b", "w:bCs", "w:i", "w:iCs", "w:caps", "w:smallCaps", "w:strike", "w:dstrike", "w:outline", "w:shadow", "w:emboss", "w:imprint", "w:noProof", "w:snapToGrid", "w:vanish", "w:webHidden", "w:color", "w:spacing", "w:w", "w:kern", "w:position", "w:sz", "w:szCs", "w:highlight", "w:u", "w:effect", "w:bdr", "w:shd", "w:fitText", "w:vertAlign", "w:rtl", "w:cs", "w:em", "w:lang", "w:eastAsianLayout", "w:specVanish", "w:oMath", "w:rPrChange"];
const PPR_ORDER = ["w:pStyle", "w:keepNext", "w:keepLines", "w:pageBreakBefore", "w:framePr", "w:widowControl", "w:numPr", "w:suppressLineNumbers", "w:pBdr", "w:shd", "w:tabs", "w:suppressAutoHyphens", "w:kinsoku", "w:wordWrap", "w:overflowPunct", "w:topLinePunct", "w:autoSpaceDE", "w:autoSpaceDN", "w:bidi", "w:adjustRightInd", "w:snapToGrid", "w:spacing", "w:ind", "w:contextualSpacing", "w:mirrorIndents", "w:suppressOverlap", "w:jc", "w:textDirection", "w:textAlignment", "w:textboxTightWrap", "w:outlineLvl", "w:divId", "w:cnfStyle", "w:rPr", "w:sectPr", "w:pPrChange"];
const TRPR_ORDER = ["w:cnfStyle", "w:divId", "w:gridBefore", "w:gridAfter", "w:wBefore", "w:wAfter", "w:cantSplit", "w:trHeight", "w:tblHeader", "w:tblCellSpacing", "w:jc", "w:hidden", "w:ins", "w:del", "w:trPrChange"];
const HIGHLIGHTS = ["yellow", "green", "cyan", "magenta", "blue", "red", "darkBlue", "darkCyan", "darkGreen", "darkMagenta", "darkRed", "darkYellow", "darkGray", "lightGray", "black", "white"];
const ALIGN = { left: "left", center: "center", centre: "center", right: "right", justify: "both", both: "both" };
const CM = 567;
const EMU_PER_CM = 360000;

function hex(color, what) {
  const match = /^#?([0-9a-f]{6})$/i.exec(String(color));
  if (!match) throw new Error(`${what} must be #RRGGBB, got ${JSON.stringify(color)}`);
  return match[1].toUpperCase();
}

function stamp(track) {
  return { "w:id": track.id(), "w:author": track.author, "w:date": track.date };
}

function rPrFor(props, ctx) {
  const rPr = x.el("w:rPr");
  const add = (node) => x.insertOrdered(rPr, node, RPR_ORDER);
  if (props.style) add(x.el("w:rStyle", { "w:val": ctx.doc.findStyle(props.style, "character")?.id ?? props.style }));
  const font = props.code ? "Consolas" : props.font;
  if (font) add(x.el("w:rFonts", { "w:ascii": font, "w:hAnsi": font, "w:cs": font, "w:eastAsia": font }));
  if (props.bold) add(x.el("w:b"));
  if (props.italic) add(x.el("w:i"));
  if (props.strike) add(x.el("w:strike"));
  if (props.color) add(x.el("w:color", { "w:val": hex(props.color, "color") }));
  if (props.size) {
    const half = Math.round(Number(props.size) * 2);
    if (!(half >= 2 && half <= 3276)) throw new Error("size must be between 1 and 1638 points");
    add(x.el("w:sz", { "w:val": half }));
    add(x.el("w:szCs", { "w:val": half }));
  }
  if (props.highlight) {
    const name = HIGHLIGHTS.find((entry) => entry.toLowerCase() === String(props.highlight).toLowerCase());
    if (!name) throw new Error(`highlight must be one of ${HIGHLIGHTS.join(", ")}`);
    add(x.el("w:highlight", { "w:val": name }));
  }
  if (props.underline) add(x.el("w:u", { "w:val": props.underline === true ? "single" : String(props.underline) }));
  if (props.superscript) add(x.el("w:vertAlign", { "w:val": "superscript" }));
  if (props.subscript) add(x.el("w:vertAlign", { "w:val": "subscript" }));
  return x.kids(rPr).length ? rPr : undefined;
}

function runChildren(text) {
  const out = [];
  const parts = String(text).split(/(\t|\r?\n)/);
  for (const part of parts) {
    if (part === "") continue;
    if (part === "\t") out.push(x.el("w:tab"));
    else if (part === "\n" || part === "\r\n") out.push(x.el("w:br"));
    else out.push(textElement("w:t", part));
  }
  return out;
}

function textRun(text, rPr) {
  const run = x.el("w:r");
  if (rPr) x.kids(run).push(rPr);
  x.kids(run).push(...runChildren(text));
  return run;
}

function normalizeRuns(content) {
  if (content === undefined || content === null) return [];
  if (typeof content === "string" || typeof content === "number") return [{ text: String(content) }];
  if (Array.isArray(content)) return content.flatMap((entry) => (typeof entry === "string" || typeof entry === "number" ? [{ text: String(entry) }] : [entry]));
  if (typeof content === "object") return [content];
  throw new Error("text must be a string or a list of runs");
}

function inlineNodes(content, ctx, base = {}) {
  const nodes = [];
  for (const spec of normalizeRuns(content)) {
    if (spec.text === undefined) throw new Error(`a run needs text: ${JSON.stringify(spec)}`);
    const props = { ...base, ...spec };
    if (spec.link) {
      const linkStyle = ctx.doc.findStyle("Hyperlink", "character")?.id ?? ctx.doc.ensureStyle("hyperlink");
      const rPr = rPrFor({ ...props, style: undefined }, ctx) ?? x.el("w:rPr");
      x.insertOrdered(rPr, x.el("w:rStyle", { "w:val": linkStyle }), RPR_ORDER);
      const target = String(spec.link);
      const attrs = target.startsWith("#") ? { "w:anchor": target.slice(1), "w:history": "1" } : { "r:id": ctx.doc.addHyperlink(target, ctx.part), "w:history": "1" };
      nodes.push(x.el("w:hyperlink", attrs, [textRun(spec.text, rPr)]));
    } else nodes.push(textRun(spec.text, rPrFor(props, ctx)));
  }
  return nodes;
}

function parseInline(text) {
  const runs = [];
  const push = (value, props) => {
    if (!value) return;
    const last = runs[runs.length - 1];
    if (last && JSON.stringify({ ...last, text: "" }) === JSON.stringify({ ...props, text: "" })) last.text += value;
    else runs.push({ ...props, text: value });
  };
  const walk = (input, props) => {
    let i = 0;
    let plain = "";
    const flush = () => {
      push(plain, props);
      plain = "";
    };
    while (i < input.length) {
      const rest = input.slice(i);
      let match;
      if (rest[0] === "\\" && i + 1 < input.length && /[\\`*_~[\]()#|>-]/.test(input[i + 1])) {
        plain += input[i + 1];
        i += 2;
        continue;
      }
      if ((match = /^`([^`]+)`/.exec(rest))) {
        flush();
        push(match[1], { ...props, code: true });
      } else if ((match = /^\[([^\]]+)\]\(([^)\s]+)\)/.exec(rest))) {
        flush();
        walk(match[1], { ...props, link: match[2] });
      } else if ((match = /^(\*\*|__)(?=\S)([\s\S]*?\S)\1/.exec(rest))) {
        flush();
        walk(match[2], { ...props, bold: true });
      } else if ((match = /^~~(?=\S)([\s\S]*?\S)~~/.exec(rest))) {
        flush();
        walk(match[1], { ...props, strike: true });
      } else if ((match = /^(\*|_)(?=\S)([\s\S]*?\S)\1(?![\w*])/.exec(rest)) && !(match[1] === "_" && /\w/.test(input[i - 1] ?? ""))) {
        flush();
        walk(match[2], { ...props, italic: true });
      } else {
        plain += input[i];
        i += 1;
        continue;
      }
      i += match[0].length;
    }
    flush();
  };
  walk(text, {});
  return runs;
}

function markdownBlocks(source) {
  const lines = String(source).replace(/\r\n?/g, "\n").split("\n");
  const blocks = [];
  let para = [];
  const flush = () => {
    if (para.length) blocks.push({ paragraph: parseInline(para.join(" ")) });
    para = [];
  };
  const isTableRow = (line) => /^\s*\|.*\|\s*$/.test(line);
  const cellsOf = (line) => line.trim().replace(/^\||\|$/g, "").split(/(?<!\\)\|/).map((cell) => parseInline(cell.trim().replace(/\\\|/g, "|")));
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    let match;
    if (/^\s*$/.test(line)) {
      flush();
      i++;
    } else if ((match = /^\s*(```|~~~)/.exec(line))) {
      flush();
      const fence = match[1];
      const body = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(fence)) body.push(lines[i++]);
      i++;
      blocks.push({ paragraph: [{ text: body.join("\n"), code: true }] });
    } else if ((match = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line))) {
      flush();
      blocks.push({ heading: parseInline(match[2]), level: match[1].length });
      i++;
    } else if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      flush();
      i++;
    } else if (/^\s*>/.test(line)) {
      flush();
      const body = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) body.push(lines[i++].replace(/^\s*>\s?/, ""));
      blocks.push({ quote: parseInline(body.join(" ")) });
    } else if (isTableRow(line) && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1])) {
      flush();
      const rows = [cellsOf(line)];
      i += 2;
      while (i < lines.length && isTableRow(lines[i])) rows.push(cellsOf(lines[i++]));
      blocks.push({ table: rows, header: true });
    } else if (/^\s*([-*+]|\d+[.)])\s+/.test(line)) {
      flush();
      const ordered = /^\s*\d/.test(line);
      const items = [];
      const stack = [{ indent: /^\s*/.exec(line)[0].replace(/\t/g, "    ").length, items }];
      while (i < lines.length && (match = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(lines[i]))) {
        const indent = match[1].replace(/\t/g, "    ").length;
        while (stack.length > 1 && indent < stack[stack.length - 1].indent) stack.pop();
        const top = stack[stack.length - 1];
        const owner = top.items[top.items.length - 1];
        const item = { text: parseInline(match[3]) };
        if (indent > top.indent && owner) {
          owner.items = owner.items ?? [];
          owner.items.push(item);
          stack.push({ indent, items: owner.items });
        } else top.items.push(item);
        i++;
      }
      blocks.push(ordered ? { numbered: items } : { bullets: items });
    } else {
      para.push(line.trim());
      i++;
    }
  }
  flush();
  return blocks;
}

function pPrFor(spec, ctx) {
  const pPr = x.el("w:pPr");
  const add = (node) => x.insertOrdered(pPr, node, PPR_ORDER);
  if (spec.styleId) add(x.el("w:pStyle", { "w:val": spec.styleId }));
  else if (spec.style) add(x.el("w:pStyle", { "w:val": ctx.doc.paragraphStyle(spec.style) }));
  if (spec.keepWithNext) add(x.el("w:keepNext"));
  if (spec.pageBreakBefore) add(x.el("w:pageBreakBefore"));
  if (spec.numId) add(x.el("w:numPr", null, [x.el("w:ilvl", { "w:val": spec.level ?? 0 }), x.el("w:numId", { "w:val": spec.numId })]));
  if (spec.spaceBefore !== undefined || spec.spaceAfter !== undefined || spec.lineSpacing !== undefined) {
    const attrs = {};
    if (spec.spaceBefore !== undefined) attrs["w:before"] = Math.round(Number(spec.spaceBefore) * 20);
    if (spec.spaceAfter !== undefined) attrs["w:after"] = Math.round(Number(spec.spaceAfter) * 20);
    if (spec.lineSpacing !== undefined) {
      attrs["w:line"] = Math.round(Number(spec.lineSpacing) * 240);
      attrs["w:lineRule"] = "auto";
    }
    add(x.el("w:spacing", attrs));
  }
  if (spec.indent !== undefined) add(x.el("w:ind", { "w:left": Math.round(Number(spec.indent) * CM) }));
  if (spec.align) {
    const value = ALIGN[String(spec.align).toLowerCase()];
    if (!value) throw new Error("align must be left, center, right or justify");
    add(x.el("w:jc", { "w:val": value }));
  }
  return pPr;
}

function paragraphNode(spec, content, ctx, base) {
  const pPr = pPrFor(spec, ctx);
  const p = x.el("w:p");
  if (x.kids(pPr).length) x.kids(p).push(pPr);
  x.kids(p).push(...inlineNodes(content, ctx, base));
  return p;
}

function imageKind(bytes) {
  if (bytes.length > 24 && bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47) {
    const view = Buffer.from(bytes);
    return { kind: "png", width: view.readUInt32BE(16), height: view.readUInt32BE(20) };
  }
  if (bytes.length > 10 && bytes[0] === 0x47 && bytes[1] === 0x49 && bytes[2] === 0x46) {
    const view = Buffer.from(bytes);
    return { kind: "gif", width: view.readUInt16LE(6), height: view.readUInt16LE(8) };
  }
  if (bytes.length > 4 && bytes[0] === 0xff && bytes[1] === 0xd8) {
    const view = Buffer.from(bytes);
    let at = 2;
    while (at + 9 < view.length) {
      if (view[at] !== 0xff) {
        at++;
        continue;
      }
      const marker = view[at + 1];
      const length = view.readUInt16BE(at + 2);
      if (marker >= 0xc0 && marker <= 0xcf && ![0xc4, 0xc8, 0xcc].includes(marker)) {
        return { kind: "jpeg", height: view.readUInt16BE(at + 5), width: view.readUInt16BE(at + 7) };
      }
      at += 2 + length;
    }
  }
  throw new Error("the image must be a PNG, JPEG or GIF file");
}

function imageNode(spec, ctx) {
  if (ctx.part && ctx.part !== ctx.doc.mainPath) throw new Error("images go in the body only, not in a header or footer");
  const file = String(spec.image);
  if (!fs.existsSync(file)) throw new Error(`no such image: ${file}`);
  const bytes = new Uint8Array(fs.readFileSync(file));
  const info = imageKind(bytes);
  if (!info.width || !info.height) throw new Error(`cannot read the size of ${file}`);
  const maxCm = ctx.doc.textWidthTwips() / CM;
  let widthCm = spec.width !== undefined ? Number(spec.width) : (info.width / 96) * 2.54;
  let heightCm = spec.height !== undefined ? Number(spec.height) : (widthCm * info.height) / info.width;
  if (spec.width === undefined && spec.height !== undefined) widthCm = (heightCm * info.width) / info.height;
  if (spec.width === undefined && widthCm > maxCm) {
    heightCm = (heightCm * maxCm) / widthCm;
    widthCm = maxCm;
  }
  if (!(widthCm > 0 && heightCm > 0)) throw new Error("image width and height must be positive numbers of centimetres");
  ctx.doc.ensureRootNamespaces();
  const rId = ctx.doc.addImage(bytes, info.kind);
  const id = ctx.doc.nextDocPrId();
  const cx = Math.round(widthCm * EMU_PER_CM);
  const cy = Math.round(heightCm * EMU_PER_CM);
  const name = T.escapeAttr(`Picture ${id}`);
  const alt = T.escapeAttr(spec.alt ?? spec.caption ?? "");
  const xml = `<w:drawing xmlns:w="${T.NS.w}" xmlns:wp="${T.NS.wp}" xmlns:a="${T.NS.a}" xmlns:pic="${T.NS.pic}" xmlns:r="${T.NS.r}"><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="${cx}" cy="${cy}"/><wp:effectExtent l="0" t="0" r="0" b="0"/><wp:docPr id="${id}" name="${name}" descr="${alt}"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="0" name="${name}"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="${rId}"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="${cx}" cy="${cy}"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing>`;
  const drawing = x.rootOf(x.parseXml(xml));
  delete drawing[":@"];
  const p = x.el("w:p");
  const pPr = pPrFor({ align: spec.align ?? "center", keepWithNext: Boolean(spec.caption) }, ctx);
  x.kids(p).push(pPr, x.el("w:r", null, [drawing]));
  const out = [p];
  if (spec.caption) out.push(paragraphNode({ styleId: ctx.doc.ensureStyle("caption"), align: spec.align ?? "center" }, spec.caption, ctx));
  return out;
}

function itemContent(item) {
  if (!item || typeof item !== "object" || Array.isArray(item)) return item;
  const { items: _children, runs, ...rest } = item;
  if (runs !== undefined) return runs;
  return Array.isArray(rest.text) ? rest.text : rest;
}

function listNodes(items, kind, ctx) {
  const numId = ctx.doc.newList(kind);
  const styleId = ctx.doc.ensureStyle("list paragraph");
  const out = [];
  const visit = (entries, level) => {
    if (level > 8) throw new Error("lists nest at most 9 levels deep");
    for (const item of entries) {
      out.push(paragraphNode({ styleId, numId, level }, itemContent(item), ctx));
      if (item && typeof item === "object" && Array.isArray(item.items)) visit(item.items, level + 1);
    }
  };
  if (!Array.isArray(items) || !items.length) throw new Error("a list needs at least one item");
  visit(items, 0);
  return out;
}

function cellSpec(cell) {
  if (cell === null || cell === undefined) return { content: "" };
  if (typeof cell === "string" || typeof cell === "number" || Array.isArray(cell)) return { content: cell };
  if (typeof cell === "object") return { ...cell, content: cell.runs ?? cell.text ?? "" };
  return { content: String(cell) };
}

function tableNode(spec, ctx) {
  const rows = spec.table;
  if (!Array.isArray(rows) || !rows.length || !rows.every(Array.isArray)) throw new Error("table must be a list of rows, each a list of cells");
  const columns = Math.max(...rows.map((row) => row.reduce((sum, cell) => sum + (Number(cellSpec(cell).colspan) || 1), 0)));
  const total = ctx.doc.textWidthTwips();
  let widths;
  if (spec.widths) {
    if (!Array.isArray(spec.widths) || spec.widths.length !== columns) throw new Error(`widths needs ${columns} numbers (centimetres)`);
    widths = spec.widths.map((cm) => Math.round(Number(cm) * CM));
  } else widths = Array.from({ length: columns }, () => Math.floor(total / columns));
  const header = spec.header !== false;
  const tblPr = x.el("w:tblPr", null, [
    x.el("w:tblStyle", { "w:val": spec.style ? ctx.doc.findStyle(spec.style, "table")?.id ?? ctx.doc.ensureStyle(String(spec.style).toLowerCase()) : ctx.doc.ensureStyle("table grid") }),
    x.el("w:tblW", { "w:w": widths.reduce((a, b) => a + b, 0), "w:type": "dxa" }),
  ]);
  if (spec.align) x.kids(tblPr).push(x.el("w:jc", { "w:val": ALIGN[String(spec.align).toLowerCase()] ?? "left" }));
  x.kids(tblPr).push(x.el("w:tblLayout", { "w:type": "fixed" }), x.el("w:tblLook", { "w:val": "04A0", "w:firstRow": header ? "1" : "0", "w:lastRow": "0", "w:firstColumn": "0", "w:lastColumn": "0", "w:noHBand": "0", "w:noVBand": "1" }));
  const tbl = x.el("w:tbl", null, [tblPr, x.el("w:tblGrid", null, widths.map((w) => x.el("w:gridCol", { "w:w": w })))]);
  rows.forEach((row, r) => {
    const tr = x.el("w:tr");
    const trPr = x.el("w:trPr");
    if (header && r === 0) x.kids(trPr).push(x.el("w:tblHeader"));
    if (x.kids(trPr).length) x.kids(tr).push(trPr);
    let col = 0;
    for (const raw of row) {
      const cell = cellSpec(raw);
      const span = Number(cell.colspan) || 1;
      const width = widths.slice(col, col + span).reduce((a, b) => a + b, 0);
      const tcPr = x.el("w:tcPr", null, [x.el("w:tcW", { "w:w": width, "w:type": "dxa" })]);
      if (span > 1) x.kids(tcPr).push(x.el("w:gridSpan", { "w:val": span }));
      if (cell.fill) x.kids(tcPr).push(x.el("w:shd", { "w:val": "clear", "w:color": "auto", "w:fill": hex(cell.fill, "fill") }));
      if (cell.valign) x.kids(tcPr).push(x.el("w:vAlign", { "w:val": cell.valign === "middle" ? "center" : cell.valign }));
      const base = { bold: cell.bold ?? (header && r === 0 ? true : undefined) };
      const paragraphs = cell.blocks ? blockNodes(cell.blocks, ctx) : [paragraphNode({ align: cell.align }, cell.content, ctx, base)];
      x.kids(tr).push(x.el("w:tc", null, [tcPr, ...paragraphs]));
      col += span;
    }
    while (col < columns) {
      x.kids(tr).push(x.el("w:tc", null, [x.el("w:tcPr", null, [x.el("w:tcW", { "w:w": widths[col], "w:type": "dxa" })]), x.el("w:p")]));
      col++;
    }
    x.kids(tbl).push(tr);
  });
  return tbl;
}

function tocNodes(spec, ctx) {
  const depth = Number(spec.depth ?? 3);
  if (!(depth >= 1 && depth <= 9)) throw new Error("toc depth must be 1 to 9");
  ctx.usedFields = true;
  const out = [];
  if (spec.title !== false) out.push(paragraphNode({ styleId: ctx.doc.ensureStyle("toc heading") }, spec.title ?? "Contents", ctx));
  const p = x.el("w:p", null, [
    x.el("w:r", null, [x.el("w:fldChar", { "w:fldCharType": "begin", "w:dirty": "true" })]),
    x.el("w:r", null, [textElement("w:instrText", ` TOC \\o "1-${depth}" \\h \\z \\u `)]),
    x.el("w:r", null, [x.el("w:fldChar", { "w:fldCharType": "separate" })]),
    textRun("Open the document in Word and update this field to build the table of contents."),
    x.el("w:r", null, [x.el("w:fldChar", { "w:fldCharType": "end" })]),
  ]);
  out.push(p);
  return out;
}

function fieldText(text, ctx) {
  const nodes = [];
  const parts = String(text).split(/(\{PAGE\}|\{PAGES\})/);
  for (const part of parts) {
    if (!part) continue;
    if (part === "{PAGE}" || part === "{PAGES}") {
      const instr = part === "{PAGE}" ? " PAGE " : " NUMPAGES ";
      nodes.push(x.el("w:fldSimple", { "w:instr": instr }, [textRun("1")]));
    } else nodes.push(...inlineNodes(part, ctx));
  }
  return nodes;
}

function blockNodes(specs, ctx) {
  if (!Array.isArray(specs)) throw new Error("blocks must be a list");
  const out = [];
  specs.forEach((spec, index) => {
    try {
      out.push(...blockNode(spec, ctx));
    } catch (error) {
      throw new Error(`block ${index + 1}: ${error.message}`);
    }
  });
  return out;
}

function blockNode(spec, ctx) {
  if (typeof spec === "string" || typeof spec === "number") return [paragraphNode({}, String(spec), ctx)];
  if (!spec || typeof spec !== "object") throw new Error("a block must be a string or an object");
  if (spec.heading !== undefined) return [paragraphNode({ ...spec, style: undefined, styleId: ctx.doc.headingStyle(Number(spec.level ?? 1)) }, spec.heading, ctx)];
  if (spec.title !== undefined && spec.toc === undefined) return [paragraphNode({ ...spec, style: undefined, styleId: ctx.doc.ensureStyle("title") }, spec.title, ctx)];
  if (spec.subtitle !== undefined) return [paragraphNode({ ...spec, style: undefined, styleId: ctx.doc.ensureStyle("subtitle") }, spec.subtitle, ctx)];
  if (spec.quote !== undefined) return [paragraphNode({ ...spec, style: undefined, styleId: ctx.doc.ensureStyle("quote") }, spec.quote, ctx)];
  if (spec.paragraph !== undefined) return [paragraphNode(spec, spec.paragraph, ctx)];
  if (spec.bullets !== undefined) return listNodes(spec.bullets, "bullet", ctx);
  if (spec.numbered !== undefined) return listNodes(spec.numbered, "number", ctx);
  if (spec.table !== undefined) {
    const tbl = tableNode(spec, ctx);
    return spec.caption ? [paragraphNode({ styleId: ctx.doc.ensureStyle("caption"), keepWithNext: true }, spec.caption, ctx), tbl] : [tbl];
  }
  if (spec.image !== undefined) return imageNode(spec, ctx);
  if (spec.pageBreak) return [x.el("w:p", null, [x.el("w:r", null, [x.el("w:br", { "w:type": "page" })])])];
  if (spec.toc) return tocNodes(spec, ctx);
  if (spec.markdown !== undefined) return blockNodes(markdownBlocks(spec.markdown), ctx);
  throw new Error(`unknown block ${JSON.stringify(spec).slice(0, 80)}; use heading, title, subtitle, paragraph, quote, bullets, numbered, table, image, pageBreak, toc or markdown`);
}

function markInserted(nodes, track) {
  const wrap = (p) => {
    let pPr = x.child(p, "w:pPr");
    if (!pPr) {
      pPr = x.el("w:pPr");
      x.kids(p).unshift(pPr);
    }
    let rPr = x.child(pPr, "w:rPr");
    if (!rPr) rPr = x.insertOrdered(pPr, x.el("w:rPr"), PPR_ORDER);
    x.insertOrdered(rPr, x.el("w:ins", stamp(track)), RPR_ORDER);
    const content = x.kids(p).filter((node) => x.tagOf(node) !== "w:pPr");
    if (content.length) {
      const list = x.kids(p);
      list.splice(list.indexOf(content[0]), content.length, x.el("w:ins", stamp(track), content));
    }
  };
  const visit = (node) => {
    const tag = x.tagOf(node);
    if (tag === "w:p") wrap(node);
    else if (tag === "w:tbl") {
      for (const tr of x.children(node, "w:tr")) {
        let trPr = x.child(tr, "w:trPr");
        if (!trPr) {
          trPr = x.el("w:trPr");
          const list = x.kids(tr);
          const at = list.findIndex((entry) => ["w:tc", "w:sdt", "w:customXml"].includes(x.tagOf(entry)));
          list.splice(at < 0 ? list.length : at, 0, trPr);
        }
        x.insertOrdered(trPr, x.el("w:ins", stamp(track)), TRPR_ORDER);
        for (const tc of x.children(tr, "w:tc")) for (const inner of x.kids(tc)) visit(inner);
      }
    }
  };
  nodes.forEach(visit);
  return nodes;
}

module.exports = {
  PPR_ORDER,
  RPR_ORDER,
  TRPR_ORDER,
  blockNodes,
  fieldText,
  imageKind,
  inlineNodes,
  markInserted,
  markdownBlocks,
  pPrFor,
  paragraphNode,
  parseInline,
  rPrFor,
  stamp,
  textRun,
};
