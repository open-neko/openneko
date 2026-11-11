"use strict";

const fs = require("node:fs");
const { zipSync, strToU8 } = require("fflate");
const x = require("./ooxml.cjs");
const T = require("./template.cjs");

const RPR_ORDER = ["a:ln", "a:noFill", "a:solidFill", "a:gradFill", "a:blipFill", "a:pattFill", "a:grpFill", "a:effectLst", "a:effectDag", "a:highlight", "a:uLnTx", "a:uLn", "a:uFillTx", "a:uFill", "a:latin", "a:ea", "a:cs", "a:sym", "a:hlinkClick", "a:hlinkMouseOver", "a:rtl", "a:extLst"];
const PPR_ORDER = ["a:lnSpc", "a:spcBef", "a:spcAft", "a:buClrTx", "a:buClr", "a:buSzTx", "a:buSzPct", "a:buSzPts", "a:buFontTx", "a:buFont", "a:buNone", "a:buAutoNum", "a:buChar", "a:buBlip", "a:tabLst", "a:defRPr", "a:extLst"];
const ALIGN = { left: "l", center: "ctr", centre: "ctr", right: "r", justify: "just" };
const VALIGN = { top: "t", middle: "ctr", center: "ctr", bottom: "b" };
const SHAPES = { rect: "rect", rectangle: "rect", roundRect: "roundRect", rounded: "roundRect", ellipse: "ellipse", oval: "ellipse", triangle: "triangle", arrow: "rightArrow", rightArrow: "rightArrow", chevron: "chevron", line: "line" };
const RUN_KEYS = ["bold", "italic", "underline", "strike", "color", "size", "font", "link", "superscript", "subscript"];

function cm(value, what) {
  const n = Number(value);
  if (!Number.isFinite(n)) throw new Error(`${what} must be a number of centimetres, got ${JSON.stringify(value)}`);
  return Math.round(n * T.EMU_PER_CM);
}

function hex(color, what) {
  const match = /^#?([0-9a-f]{6})$/i.exec(String(color));
  if (!match) throw new Error(`${what} must be #RRGGBB, got ${JSON.stringify(color)}`);
  return match[1].toUpperCase();
}

function solidFill(color, what) {
  return x.el("a:solidFill", null, [x.el("a:srgbClr", { val: hex(color, what) })]);
}

function normalizeRuns(content) {
  if (content === undefined || content === null) return [];
  if (typeof content === "string" || typeof content === "number") return [{ text: String(content) }];
  if (Array.isArray(content)) return content.flatMap((entry) => (typeof entry === "object" && entry !== null ? [entry] : [{ text: String(entry) }]));
  if (typeof content === "object") return [content];
  throw new Error(`text must be a string or a list of runs, got ${JSON.stringify(content)}`);
}

function rPrFor(run, ctx) {
  const a = { lang: "en-US" };
  if (run.size !== undefined) {
    const size = Number(run.size);
    if (!(size >= 1 && size <= 400)) throw new Error(`size must be 1 to 400 points, got ${JSON.stringify(run.size)}`);
    a.sz = Math.round(size * 100);
  }
  if (run.bold !== undefined) a.b = run.bold ? 1 : 0;
  if (run.italic !== undefined) a.i = run.italic ? 1 : 0;
  if (run.underline) a.u = "sng";
  if (run.strike) a.strike = "sngStrike";
  if (run.superscript) a.baseline = 30000;
  if (run.subscript) a.baseline = -25000;
  a.dirty = 0;
  const rPr = x.el("a:rPr", a);
  if (run.color) x.insertOrdered(rPr, solidFill(run.color, "color"), RPR_ORDER);
  if (run.font) x.insertOrdered(rPr, x.el("a:latin", { typeface: String(run.font) }), RPR_ORDER);
  if (run.link) {
    if (!ctx?.part) throw new Error("a link needs a slide");
    const url = String(run.link);
    if (!/^(https?:|mailto:)/i.test(url)) throw new Error(`a link must start with http, https or mailto, got ${JSON.stringify(url)}`);
    x.insertOrdered(rPr, x.el("a:hlinkClick", { "r:id": ctx.deck.linkRel(ctx.part, url) }), RPR_ORDER);
  }
  return rPr;
}

function runNodes(content, ctx, base = {}) {
  const out = [];
  for (const run of normalizeRuns(content)) {
    if (typeof run.text !== "string" && typeof run.text !== "number") throw new Error(`a run needs text, got ${JSON.stringify(run)}`);
    const props = { ...base, ...run };
    const pieces = String(run.text).split("\n");
    pieces.forEach((piece, i) => {
      if (i > 0) out.push(x.el("a:br", null, [rPrFor(props, ctx)]));
      if (piece) out.push(x.el("a:r", null, [rPrFor(props, ctx), x.el("a:t", null, [x.textNode(piece)])]));
    });
  }
  return out;
}

function paragraphItems(content) {
  if (content === undefined || content === null) return [];
  if (typeof content === "string" || typeof content === "number") return String(content).split("\n").map((line) => ({ text: line }));
  if (!Array.isArray(content)) {
    if (typeof content === "object" && "text" in content) return [content];
    throw new Error(`text must be a string, a list of paragraphs, or { "text": ... }, got ${JSON.stringify(content)}`);
  }
  const out = [];
  const visit = (items, level) => {
    for (const item of items) {
      if (typeof item === "string" || typeof item === "number") out.push({ text: String(item), level });
      else if (Array.isArray(item)) out.push({ text: item, level });
      else if (item && typeof item === "object") {
        if (item.text === undefined && !item.items) throw new Error(`a paragraph needs text, got ${JSON.stringify(item)}`);
        if (item.text !== undefined) out.push({ ...item, level: item.level ?? level });
        if (Array.isArray(item.items)) visit(item.items, (item.level ?? level) + 1);
      } else throw new Error(`a paragraph must be text or an object, got ${JSON.stringify(item)}`);
    }
  };
  visit(content, 0);
  return out;
}

function pPrFor(item, options) {
  const a = {};
  const level = Number(item.level ?? 0);
  if (!(Number.isInteger(level) && level >= 0 && level <= 8)) throw new Error(`level must be 0 to 8, got ${JSON.stringify(item.level)}`);
  if (level) a.lvl = level;
  const align = item.align ?? options.align;
  if (align !== undefined) {
    if (!ALIGN[align]) throw new Error(`align must be left, center, right or justify, got ${JSON.stringify(align)}`);
    a.algn = ALIGN[align];
  }
  const bullet = item.bullet !== undefined ? item.bullet : options.bullets;
  const pPr = x.el("a:pPr", a);
  if (bullet === "number" || bullet === "numbered") {
    a.marL = 342900 + level * 342900;
    a.indent = -342900;
    x.insertOrdered(pPr, x.el("a:buFont", { typeface: "+mj-lt" }), PPR_ORDER);
    x.insertOrdered(pPr, x.el("a:buAutoNum", { type: "arabicPeriod" }), PPR_ORDER);
  } else if (bullet === true) {
    a.marL = 285750 + level * 285750;
    a.indent = -285750;
    x.insertOrdered(pPr, x.el("a:buFont", { typeface: "Arial" }), PPR_ORDER);
    x.insertOrdered(pPr, x.el("a:buChar", { char: "•" }), PPR_ORDER);
  } else if (bullet === false) {
    a.marL = 0;
    a.indent = 0;
    x.insertOrdered(pPr, x.el("a:buNone"), PPR_ORDER);
  } else if (bullet !== undefined && bullet !== null) {
    throw new Error(`bullet must be true, false or "number", got ${JSON.stringify(bullet)}`);
  }
  const spacing = item.spaceBefore ?? options.spaceBefore;
  if (spacing !== undefined) x.insertOrdered(pPr, x.el("a:spcBef", null, [x.el("a:spcPts", { val: Math.round(Number(spacing) * 100) })]), PPR_ORDER);
  if (pPr[":@"] && !Object.keys(pPr[":@"]).length) delete pPr[":@"];
  return pPr;
}

function paragraphNodes(content, ctx, options = {}) {
  const items = paragraphItems(content);
  if (!items.length) return [x.el("a:p", null, [x.el("a:endParaRPr", { lang: "en-US", dirty: 0 })])];
  return items.map((item) => {
    const base = {};
    for (const key of RUN_KEYS) {
      if (item[key] !== undefined) base[key] = item[key];
      else if (options[key] !== undefined) base[key] = options[key];
    }
    const p = x.el("a:p");
    const pPr = pPrFor(item, options);
    if (pPr[":@"] || x.kids(pPr).length) x.kids(p).push(pPr);
    const runs = runNodes(item.text, ctx, base);
    x.kids(p).push(...runs);
    if (!runs.length) x.kids(p).push(x.el("a:endParaRPr", { lang: "en-US", dirty: 0, ...(base.size ? { sz: Math.round(Number(base.size) * 100) } : {}) }));
    return p;
  });
}

function txBody(content, ctx, options = {}, tag = "p:txBody") {
  const bodyPr = x.el("a:bodyPr", options.bodyPr ?? {});
  if (options.autofit === "shrink") x.kids(bodyPr).push(x.el("a:normAutofit"));
  else if (options.autofit === "resize") x.kids(bodyPr).push(x.el("a:spAutoFit"));
  return x.el(tag, null, [bodyPr, x.el("a:lstStyle"), ...paragraphNodes(content, ctx, options)]);
}

function xfrmNode(tag, box) {
  return x.el(tag, null, [x.el("a:off", { x: Math.round(box.x), y: Math.round(box.y) }), x.el("a:ext", { cx: Math.max(0, Math.round(box.w)), cy: Math.max(0, Math.round(box.h)) })]);
}

function boxFrom(spec, fallback, what) {
  const box = { ...(fallback ?? {}) };
  for (const key of ["x", "y", "w", "h"]) if (spec[key] !== undefined) box[key] = cm(spec[key], `${what} ${key}`);
  for (const key of ["x", "y", "w", "h"]) if (box[key] === undefined) throw new Error(`${what} needs x, y, w and h in centimetres`);
  if (box.w <= 0 || box.h <= 0) throw new Error(`${what} needs a w and h above 0`);
  return box;
}

function nameAttrs(id, name, alt) {
  const a = { id, name };
  if (alt) a.descr = String(alt);
  return a;
}

function textShape(spec, ctx, box) {
  const id = ctx.ids.next();
  const geometry = spec.shape ?? "rect";
  const prst = SHAPES[geometry];
  if (!prst) throw new Error(`shape must be one of ${Object.keys(SHAPES).join(", ")}, got ${JSON.stringify(geometry)}`);
  const isBox = prst === "rect" && !spec.fill && !spec.line;
  const spPr = x.el("p:spPr", null, [xfrmNode("a:xfrm", box), x.el("a:prstGeom", { prst }, [x.el("a:avLst")])]);
  if (spec.fill) x.kids(spPr).push(solidFill(spec.fill, "fill"));
  else if (isBox || spec.fill === false) x.kids(spPr).push(x.el("a:noFill"));
  if (spec.line) {
    const line = typeof spec.line === "object" ? spec.line : { color: spec.line };
    x.kids(spPr).push(x.el("a:ln", { w: Math.round(Number(line.width ?? 1) * 12700) }, [solidFill(line.color ?? "#000000", "line color")]));
  } else if (prst !== "line") x.kids(spPr).push(x.el("a:ln", null, [x.el("a:noFill")]));
  const bodyPr = { wrap: "square", rtlCol: 0 };
  if (spec.valign !== undefined) {
    if (!VALIGN[spec.valign]) throw new Error("valign must be top, middle or bottom");
    bodyPr.anchor = VALIGN[spec.valign];
  } else if (!isBox) bodyPr.anchor = "ctr";
  if (spec.margin !== undefined) for (const side of ["lIns", "rIns", "tIns", "bIns"]) bodyPr[side] = cm(spec.margin, "margin");
  const textColor = spec.color ?? (spec.fill && !isBox ? undefined : undefined);
  const options = { bodyPr, align: spec.align ?? (isBox ? undefined : "center"), bullets: spec.bullets, size: spec.size, bold: spec.bold, italic: spec.italic, color: textColor, font: spec.font, autofit: spec.autofit };
  const nv = x.el("p:nvSpPr", null, [x.el("p:cNvPr", nameAttrs(id, spec.name ?? `${isBox ? "TextBox" : "Shape"} ${id - 1}`, spec.alt)), x.el("p:cNvSpPr", isBox ? { txBox: 1 } : null), x.el("p:nvPr")]);
  const children = [nv, spPr];
  if (!isBox) children.push(styleRef());
  children.push(txBody(spec.text ?? "", ctx, options));
  return x.el("p:sp", null, children);
}

function styleRef() {
  return x.el("p:style", null, [
    x.el("a:lnRef", { idx: 2 }, [x.el("a:schemeClr", { val: "accent1" }, [x.el("a:shade", { val: 50000 })])]),
    x.el("a:fillRef", { idx: 1 }, [x.el("a:schemeClr", { val: "accent1" })]),
    x.el("a:effectRef", { idx: 0 }, [x.el("a:schemeClr", { val: "accent1" })]),
    x.el("a:fontRef", { idx: "minor" }, [x.el("a:schemeClr", { val: "lt1" })]),
  ]);
}

function imageKind(bytes) {
  const view = Buffer.from(bytes);
  if (view.length > 24 && view[0] === 0x89 && view[1] === 0x50 && view[2] === 0x4e && view[3] === 0x47) return { kind: "png", width: view.readUInt32BE(16), height: view.readUInt32BE(20) };
  if (view.length > 10 && view[0] === 0x47 && view[1] === 0x49 && view[2] === 0x46) return { kind: "gif", width: view.readUInt16LE(6), height: view.readUInt16LE(8) };
  if (view.length > 4 && view[0] === 0xff && view[1] === 0xd8) {
    let at = 2;
    while (at + 9 < view.length) {
      if (view[at] !== 0xff) {
        at++;
        continue;
      }
      const marker = view[at + 1];
      if (marker >= 0xc0 && marker <= 0xcf && ![0xc4, 0xc8, 0xcc].includes(marker)) return { kind: "jpeg", height: view.readUInt16BE(at + 5), width: view.readUInt16BE(at + 7) };
      at += 2 + view.readUInt16BE(at + 2);
    }
  }
  throw new Error("the image must be a PNG, JPEG or GIF file");
}

function loadImage(file) {
  const path = String(file);
  if (!fs.existsSync(path)) throw new Error(`no such image: ${path}`);
  const bytes = new Uint8Array(fs.readFileSync(path));
  const info = imageKind(bytes);
  if (!(info.width > 0 && info.height > 0)) throw new Error(`cannot read the size of ${path}`);
  return { bytes, ...info };
}

function fitBox(box, width, height, mode) {
  const ratio = width / height;
  if (mode === "stretch") return { box, crop: null };
  if (mode === "fill") {
    const boxRatio = box.w / box.h;
    if (ratio > boxRatio) {
      const keep = boxRatio / ratio;
      const cut = Math.round(((1 - keep) / 2) * 100000);
      return { box, crop: { l: cut, r: cut } };
    }
    const keep = ratio / boxRatio;
    const cut = Math.round(((1 - keep) / 2) * 100000);
    return { box, crop: { t: cut, b: cut } };
  }
  let w = box.w;
  let h = w / ratio;
  if (h > box.h) {
    h = box.h;
    w = h * ratio;
  }
  return { box: { x: box.x + (box.w - w) / 2, y: box.y + (box.h - h) / 2, w, h }, crop: null };
}

function pictureShape(spec, ctx, area, placeholder) {
  const image = loadImage(spec.image);
  const media = ctx.deck.addMedia(image.bytes, image.kind === "jpeg" ? "jpeg" : image.kind);
  const rid = ctx.deck.relate(ctx.part, T.REL_TYPES.image, media);
  const id = ctx.ids.next();
  let box;
  let crop = null;
  if (spec.x !== undefined || spec.y !== undefined || spec.w !== undefined || spec.h !== undefined) {
    const x0 = spec.x !== undefined ? cm(spec.x, "image x") : area?.x;
    const y0 = spec.y !== undefined ? cm(spec.y, "image y") : area?.y;
    if (x0 === undefined || y0 === undefined) throw new Error("an image needs x and y in centimetres");
    let w = spec.w !== undefined ? cm(spec.w, "image w") : undefined;
    let h = spec.h !== undefined ? cm(spec.h, "image h") : undefined;
    if (w === undefined && h === undefined) w = Math.round(image.width * 9525);
    if (w === undefined) w = Math.round((h * image.width) / image.height);
    if (h === undefined) h = Math.round((w * image.height) / image.width);
    if (spec.w !== undefined && spec.h !== undefined && spec.fit) ({ box, crop } = fitBox({ x: x0, y: y0, w, h }, image.width, image.height, spec.fit));
    else box = { x: x0, y: y0, w, h };
  } else {
    if (!area) throw new Error("an image needs x and y, or a placeholder to fill");
    ({ box, crop } = fitBox(area, image.width, image.height, spec.fit ?? (placeholder ? "fill" : "fit")));
  }
  const nvPr = x.el("p:nvPr");
  if (placeholder && !(spec.x !== undefined || spec.y !== undefined)) x.kids(nvPr).push(x.el("p:ph", placeholder.phAttrs));
  const blipFill = x.el("p:blipFill", null, [x.el("a:blip", { "r:embed": rid })]);
  if (crop) x.kids(blipFill).push(x.el("a:srcRect", crop));
  x.kids(blipFill).push(x.el("a:stretch", null, [x.el("a:fillRect")]));
  return x.el("p:pic", null, [
    x.el("p:nvPicPr", null, [x.el("p:cNvPr", nameAttrs(id, spec.name ?? `Picture ${id - 1}`, spec.alt ?? "")), x.el("p:cNvPicPr", null, [x.el("a:picLocks", { noChangeAspect: 1 })]), nvPr]),
    blipFill,
    x.el("p:spPr", null, [xfrmNode("a:xfrm", box), x.el("a:prstGeom", { prst: "rect" }, [x.el("a:avLst")])]),
  ]);
}

function cellSpec(cell) {
  if (cell === null || cell === undefined) return { text: "" };
  if (typeof cell === "object" && !Array.isArray(cell)) return cell;
  if (typeof cell === "number") return { text: String(cell), numeric: true };
  return { text: cell };
}

function cellNode(cell, ctx, options) {
  const spec = cellSpec(cell);
  const align = spec.align ?? (spec.numeric ? "right" : undefined);
  const tc = x.el("a:tc", spec.colspan > 1 ? { gridSpan: spec.colspan } : null, [
    txBody(spec.text ?? "", ctx, { align, size: spec.size ?? options.size, bold: spec.bold ?? options.bold, italic: spec.italic, color: spec.color ?? options.color, font: spec.font }, "a:txBody"),
  ]);
  const tcPr = x.el("a:tcPr", { anchor: "ctr" });
  if (spec.fill) x.kids(tcPr).push(solidFill(spec.fill, "cell fill"));
  x.kids(tc).push(tcPr);
  return tc;
}

function tableShape(spec, ctx, area) {
  const rows = spec.table;
  if (!Array.isArray(rows) || !rows.length || !rows.every(Array.isArray)) throw new Error("table must be a list of rows, and each row a list of cells");
  const span = (row) => row.reduce((sum, cell) => sum + Math.max(1, Number(cellSpec(cell).colspan ?? 1)), 0);
  const columns = Math.max(...rows.map(span));
  const box = boxFrom(spec, { ...(area ? { x: area.x, y: area.y, w: area.w } : {}), h: area?.h ?? 1 }, "a table");
  const fontSize = Number(spec.size ?? 14);
  const rowHeight = spec.h !== undefined ? Math.round(box.h / rows.length) : Math.round(fontSize * 12700 * 2.1);
  let widths;
  if (spec.widths) {
    if (!Array.isArray(spec.widths) || spec.widths.length !== columns) throw new Error(`widths must list ${columns} numbers`);
    const total = spec.widths.reduce((sum, w) => sum + Number(w), 0);
    widths = spec.widths.map((w) => Math.round((Number(w) / total) * box.w));
  } else widths = Array(columns).fill(Math.round(box.w / columns));
  const header = spec.header !== false;
  const tblPr = x.el("a:tblPr", { firstRow: header ? 1 : 0, bandRow: spec.banded === false ? 0 : 1 }, [x.el("a:tableStyleId", null, [x.textNode(spec.style ?? T.TABLE_STYLE)])]);
  const grid = x.el("a:tblGrid", null, widths.map((w) => x.el("a:gridCol", { w })));
  const trs = rows.map((row) => {
    const cells = [];
    for (const cell of row) {
      const c = cellSpec(cell);
      cells.push(cellNode(cell, ctx, { size: fontSize }));
      for (let i = 1; i < Math.max(1, Number(c.colspan ?? 1)); i++) cells.push(x.el("a:tc", { hMerge: 1 }, [txBody("", ctx, {}, "a:txBody"), x.el("a:tcPr")]));
    }
    while (cells.length < columns) cells.push(cellNode("", ctx, { size: fontSize }));
    return x.el("a:tr", { h: rowHeight }, cells);
  });
  const id = ctx.ids.next();
  return x.el("p:graphicFrame", null, [
    x.el("p:nvGraphicFramePr", null, [x.el("p:cNvPr", nameAttrs(id, spec.name ?? `Table ${id - 1}`, spec.alt)), x.el("p:cNvGraphicFramePr", null, [x.el("a:graphicFrameLocks", { noGrp: 1 })]), x.el("p:nvPr")]),
    xfrmNode("p:xfrm", { ...box, h: rowHeight * rows.length }),
    x.el("a:graphic", null, [x.el("a:graphicData", { uri: "http://schemas.openxmlformats.org/drawingml/2006/table" }, [x.el("a:tbl", null, [tblPr, grid, ...trs])])]),
  ]);
}

const CHART_KINDS = {
  column: { plot: "c:barChart", dir: "col", grouping: "clustered" },
  bar: { plot: "c:barChart", dir: "bar", grouping: "clustered" },
  stackedColumn: { plot: "c:barChart", dir: "col", grouping: "stacked" },
  stackedBar: { plot: "c:barChart", dir: "bar", grouping: "stacked" },
  line: { plot: "c:lineChart", grouping: "standard" },
  pie: { plot: "c:pieChart" },
  doughnut: { plot: "c:doughnutChart" },
};

function column(n) {
  let out = "";
  for (let i = n + 1; i > 0; i = Math.floor((i - 1) / 26)) out = String.fromCharCode(65 + ((i - 1) % 26)) + out;
  return out;
}

function escapeXml(text) {
  return String(text).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function chartData(spec) {
  const categories = spec.categories;
  if (!Array.isArray(categories) || !categories.length) throw new Error("a chart needs a categories list");
  const series = spec.series;
  if (!Array.isArray(series) || !series.length) throw new Error("a chart needs a series list of { name, values }");
  series.forEach((s, i) => {
    if (!s || !Array.isArray(s.values)) throw new Error(`series ${i + 1} needs a values list`);
    if (s.values.length !== categories.length) throw new Error(`series ${i + 1} has ${s.values.length} values for ${categories.length} categories`);
    for (const v of s.values) if (v !== null && !Number.isFinite(Number(v))) throw new Error(`series ${i + 1} holds a value that is not a number: ${JSON.stringify(v)}`);
  });
  return { categories: categories.map(String), series: series.map((s, i) => ({ name: String(s.name ?? `Series ${i + 1}`), values: s.values.map((v) => (v === null ? null : Number(v))), color: s.color })) };
}

function workbookBytes(data) {
  const rows = [[""].concat(data.series.map((s) => s.name))];
  data.categories.forEach((c, i) => rows.push([c, ...data.series.map((s) => s.values[i])]));
  const sheet = rows.map((row, r) => `<row r="${r + 1}">${row.map((v, c) => {
    const ref = `${column(c)}${r + 1}`;
    if (v === null || v === "") return "";
    if (typeof v === "number") return `<c r="${ref}"><v>${v}</v></c>`;
    return `<c r="${ref}" t="inlineStr"><is><t xml:space="preserve">${escapeXml(v)}</t></is></c>`;
  }).join("")}</row>`).join("");
  const SML = "http://schemas.openxmlformats.org/spreadsheetml/2006/main";
  const files = {
    "[Content_Types].xml": `${T.HEAD}<Types xmlns="${T.NS.ct}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/></Types>`,
    "_rels/.rels": `${T.HEAD}<Relationships xmlns="${T.NS.rel}"><Relationship Id="rId1" Type="${T.REL_TYPES.document}" Target="xl/workbook.xml"/></Relationships>`,
    "xl/workbook.xml": `${T.HEAD}<workbook xmlns="${SML}" xmlns:r="${T.NS.r}"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
    "xl/_rels/workbook.xml.rels": `${T.HEAD}<Relationships xmlns="${T.NS.rel}"><Relationship Id="rId1" Type="${T.REL}/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
    "xl/worksheets/sheet1.xml": `${T.HEAD}<worksheet xmlns="${SML}"><sheetData>${sheet}</sheetData></worksheet>`,
  };
  return zipSync(Object.fromEntries(Object.entries(files).map(([name, text]) => [name, strToU8(text)])), { level: 6 });
}

function strCache(values) {
  return x.el("c:strCache", null, [x.el("c:ptCount", { val: values.length }), ...values.map((v, i) => x.el("c:pt", { idx: i }, [x.el("c:v", null, [x.textNode(v)])]))]);
}

function numCache(values, format) {
  const pts = [];
  values.forEach((v, i) => {
    if (v !== null) pts.push(x.el("c:pt", { idx: i }, [x.el("c:v", null, [x.textNode(String(v))])]));
  });
  return x.el("c:numCache", null, [x.el("c:formatCode", null, [x.textNode(format)]), x.el("c:ptCount", { val: values.length }), ...pts]);
}

function seriesRefs(data, s, format) {
  const last = data.categories.length + 1;
  const col = column(s + 1);
  return {
    tx: x.el("c:tx", null, [x.el("c:strRef", null, [x.el("c:f", null, [x.textNode(`Sheet1!$${col}$1`)]), strCache([data.series[s].name])])]),
    cat: x.el("c:cat", null, [x.el("c:strRef", null, [x.el("c:f", null, [x.textNode(`Sheet1!$A$2:$A$${last}`)]), strCache(data.categories)])]),
    val: x.el("c:val", null, [x.el("c:numRef", null, [x.el("c:f", null, [x.textNode(`Sheet1!$${col}$2:$${col}$${last}`)]), numCache(data.series[s].values, format)])]),
  };
}

function colorNode(series, s) {
  return series.color ? solidFill(series.color, "series color") : x.el("a:solidFill", null, [x.el("a:schemeClr", { val: `accent${(s % 6) + 1}` })]);
}

function dataLabels(spec, kind) {
  const flag = (name, on) => x.el(`c:${name}`, { val: on ? 1 : 0 });
  const pie = kind.plot === "c:pieChart" || kind.plot === "c:doughnutChart";
  const children = [];
  if (spec.numberFormat && !(pie && spec.percent)) children.push(x.el("c:numFmt", { formatCode: spec.numberFormat, sourceLinked: 0 }));
  children.push(flag("showLegendKey", false), flag("showVal", !(pie && spec.percent)), flag("showCatName", false), flag("showSerName", false), flag("showPercent", pie && spec.percent), flag("showBubbleSize", false));
  return x.el("c:dLbls", null, children);
}

function chartXml(spec) {
  const kind = CHART_KINDS[spec.chart];
  if (!kind) throw new Error(`chart must be one of ${Object.keys(CHART_KINDS).join(", ")}, got ${JSON.stringify(spec.chart)}`);
  const data = chartData(spec);
  const pie = kind.plot === "c:pieChart" || kind.plot === "c:doughnutChart";
  if (pie && data.series.length !== 1) throw new Error(`a ${spec.chart} chart takes one series`);
  const format = spec.numberFormat ?? "General";
  const series = data.series.map((s, i) => {
    const refs = seriesRefs(data, i, format);
    const children = [x.el("c:idx", { val: i }), x.el("c:order", { val: i }), refs.tx];
    if (kind.plot === "c:lineChart") {
      const fill = colorNode(s, i);
      children.push(x.el("c:spPr", null, [x.el("a:ln", { w: 28575, cap: "rnd" }, [fill, x.el("a:round")])]), x.el("c:marker", null, [x.el("c:symbol", { val: "none" })]));
    } else if (!pie) children.push(x.el("c:spPr", null, [colorNode(s, i)]), x.el("c:invertIfNegative", { val: 0 }));
    if (pie) {
      data.categories.forEach((_, p) => children.push(x.el("c:dPt", null, [x.el("c:idx", { val: p }), x.el("c:bubble3D", { val: 0 }), x.el("c:spPr", null, [x.el("a:solidFill", null, [x.el("a:schemeClr", { val: `accent${(p % 6) + 1}` })])])])));
    }
    children.push(refs.cat, refs.val);
    if (kind.plot === "c:lineChart") children.push(x.el("c:smooth", { val: 0 }));
    return x.el("c:ser", null, children);
  });
  const plotChildren = [];
  if (kind.plot === "c:barChart") plotChildren.push(x.el("c:barDir", { val: kind.dir }), x.el("c:grouping", { val: kind.grouping }));
  if (kind.plot === "c:lineChart") plotChildren.push(x.el("c:grouping", { val: kind.grouping }));
  plotChildren.push(x.el("c:varyColors", { val: pie ? 1 : 0 }), ...series);
  if (spec.labels) plotChildren.push(dataLabels(spec, kind));
  if (kind.plot === "c:barChart") {
    plotChildren.push(x.el("c:gapWidth", { val: 80 }));
    if (kind.grouping === "stacked") plotChildren.push(x.el("c:overlap", { val: 100 }));
  }
  if (kind.plot === "c:lineChart") plotChildren.push(x.el("c:marker", { val: 1 }));
  if (pie) plotChildren.push(x.el("c:firstSliceAng", { val: 0 }));
  if (kind.plot === "c:doughnutChart") plotChildren.push(x.el("c:holeSize", { val: 55 }));
  if (!pie) plotChildren.push(x.el("c:axId", { val: 111111111 }), x.el("c:axId", { val: 222222222 }));
  const plotArea = x.el("c:plotArea", null, [x.el("c:layout"), x.el(kind.plot, null, plotChildren)]);
  if (!pie) {
    const catPos = kind.dir === "bar" ? "l" : "b";
    const valPos = kind.dir === "bar" ? "b" : "l";
    const noTicks = [x.el("c:majorTickMark", { val: "none" }), x.el("c:minorTickMark", { val: "none" }), x.el("c:tickLblPos", { val: "nextTo" })];
    const gridLine = x.el("c:majorGridlines", null, [x.el("c:spPr", null, [x.el("a:ln", { w: 9525 }, [x.el("a:solidFill", null, [x.el("a:schemeClr", { val: "tx1" }, [x.el("a:lumMod", { val: 15000 }), x.el("a:lumOff", { val: 85000 })])])])])]);
    x.kids(plotArea).push(
      x.el("c:catAx", null, [x.el("c:axId", { val: 111111111 }), x.el("c:scaling", null, [x.el("c:orientation", { val: kind.dir === "bar" ? "maxMin" : "minMax" })]), x.el("c:delete", { val: 0 }), x.el("c:axPos", { val: catPos }), x.el("c:numFmt", { formatCode: "General", sourceLinked: 1 }), ...noTicks.map((n) => JSON.parse(JSON.stringify(n))), x.el("c:crossAx", { val: 222222222 }), x.el("c:crosses", { val: "autoZero" }), x.el("c:auto", { val: 1 }), x.el("c:lblAlgn", { val: "ctr" }), x.el("c:lblOffset", { val: 100 }), x.el("c:noMultiLvlLbl", { val: 0 })]),
      x.el("c:valAx", null, [x.el("c:axId", { val: 222222222 }), x.el("c:scaling", null, [x.el("c:orientation", { val: "minMax" })]), x.el("c:delete", { val: spec.axis === false ? 1 : 0 }), x.el("c:axPos", { val: valPos }), gridLine, x.el("c:numFmt", { formatCode: format, sourceLinked: 0 }), ...noTicks, x.el("c:crossAx", { val: 111111111 }), x.el("c:crosses", { val: kind.dir === "bar" ? "max" : "autoZero" }), x.el("c:crossBetween", { val: "between" })]),
    );
  }
  const chart = x.el("c:chart");
  if (spec.title) {
    x.kids(chart).push(x.el("c:title", null, [x.el("c:tx", null, [x.el("c:rich", null, [x.el("a:bodyPr"), x.el("a:lstStyle"), x.el("a:p", null, [x.el("a:pPr", null, [x.el("a:defRPr", { sz: 1600, b: 1 })]), x.el("a:r", null, [x.el("a:rPr", { lang: "en-US" }), x.el("a:t", null, [x.textNode(String(spec.title))])])])])]), x.el("c:overlay", { val: 0 })]), x.el("c:autoTitleDeleted", { val: 0 }));
  } else x.kids(chart).push(x.el("c:autoTitleDeleted", { val: 1 }));
  x.kids(chart).push(plotArea);
  const legend = spec.legend ?? (pie || data.series.length > 1);
  if (legend) x.kids(chart).push(x.el("c:legend", null, [x.el("c:legendPos", { val: typeof legend === "string" ? { bottom: "b", top: "t", left: "l", right: "r" }[legend] ?? "b" : "b" }), x.el("c:overlay", { val: 0 })]));
  x.kids(chart).push(x.el("c:plotVisOnly", { val: 1 }), x.el("c:dispBlanksAs", { val: "gap" }));
  const size = Math.round(Number(spec.size ?? 12) * 100);
  const txPr = x.el("c:txPr", null, [x.el("a:bodyPr"), x.el("a:lstStyle"), x.el("a:p", null, [x.el("a:pPr", null, [x.el("a:defRPr", { sz: size })]), x.el("a:endParaRPr", { lang: "en-US" })])]);
  const root = x.el("c:chartSpace", { "xmlns:c": T.NS.c, "xmlns:a": T.NS.a, "xmlns:r": T.NS.r }, [x.el("c:roundedCorners", { val: 0 }), chart, txPr, x.el("c:externalData", { "r:id": "rId1" }, [x.el("c:autoUpdate", { val: 0 })])]);
  return { tree: [root], data };
}

function chartShape(spec, ctx, area) {
  const box = boxFrom(spec, area, "a chart");
  const { tree, data } = chartXml(spec);
  const deck = ctx.deck;
  const chartPath = deck.freePart("ppt/charts", "chart", "xml");
  deck.pkg.setXml(chartPath, tree);
  deck.pkg.setOverride(chartPath, T.CONTENT_TYPES.chart);
  const book = deck.freePart("ppt/embeddings", "Microsoft_Excel_Worksheet", "xlsx");
  deck.pkg.files.set(book, workbookBytes(data));
  deck.ensureDefault("xlsx", T.CONTENT_TYPES.xlsx);
  deck.relate(chartPath, T.REL_TYPES.package, book);
  const rid = deck.relate(ctx.part, T.REL_TYPES.chart, chartPath);
  const id = ctx.ids.next();
  return x.el("p:graphicFrame", null, [
    x.el("p:nvGraphicFramePr", null, [x.el("p:cNvPr", nameAttrs(id, spec.name ?? `Chart ${id - 1}`, spec.alt)), x.el("p:cNvGraphicFramePr"), x.el("p:nvPr")]),
    xfrmNode("p:xfrm", box),
    x.el("a:graphic", null, [x.el("a:graphicData", { uri: "http://schemas.openxmlformats.org/drawingml/2006/chart" }, [x.el("c:chart", { "xmlns:c": T.NS.c, "r:id": rid })])]),
  ]);
}

function freeShape(spec, ctx, area) {
  if (!spec || typeof spec !== "object" || Array.isArray(spec)) throw new Error(`a shape must be an object, got ${JSON.stringify(spec)}`);
  if (spec.image !== undefined) return pictureShape(spec, ctx, area);
  if (spec.table !== undefined) return tableShape(spec, ctx, area);
  if (spec.chart !== undefined) return chartShape(spec, ctx, area);
  if (spec.text !== undefined || spec.shape !== undefined) return textShape(spec, ctx, boxFrom(spec, area, "a text shape"));
  throw new Error(`a shape needs text, image, table or chart, got ${JSON.stringify(Object.keys(spec))}`);
}

module.exports = { CHART_KINDS, RUN_KEYS, chartData, chartShape, chartXml, cm, fitBox, freeShape, loadImage, hex, numCache, paragraphNodes, pictureShape, seriesRefs, solidFill, strCache, tableShape, txBody, workbookBytes, xfrmNode };
