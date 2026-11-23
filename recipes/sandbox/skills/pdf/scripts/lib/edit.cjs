"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { PDFDocument, StandardFonts, degrees } = require("pdf-lib");
const { cm, fail, loadForWrite, pageSize, parseRanges } = require("./common.cjs");
const { clean, cover, draw, fallbackFor, family, measure, pick, setOutline } = require("./fonts.cjs");
const { color, setProperties } = require("./build.cjs");
const { copyAcroForm, fill, pruneFields } = require("./forms.cjs");

const POSITIONS = ["top-left", "top-center", "top-right", "bottom-left", "bottom-center", "bottom-right"];

function removePage(doc, index) {
  doc.removePage(index);
  doc.pageCache.invalidate();
}

async function copyInto(target, source, indices, at) {
  const copied = await target.copyPages(source, indices);
  copied.forEach((page, i) => (at === undefined ? target.addPage(page) : target.insertPage(at + i, page)));
  const renamed = copyAcroForm(target, source, copied);
  return { copied, renamed };
}

async function merge(inputs, options = {}) {
  if (inputs.length < 1) fail("merge needs at least one input file");
  const out = await PDFDocument.create();
  const outline = [];
  const renamed = [];
  for (const input of inputs) {
    const { file, pages } = splitInput(input);
    const source = await loadForWrite(file, options);
    const indices = parseRanges(pages, source.getPageCount(), `pages of ${path.basename(file)}`).map((n) => n - 1);
    const first = out.getPageCount();
    const result = await copyInto(out, source, indices);
    renamed.push(...result.renamed.map((r) => ({ ...r, file })));
    outline.push({ title: path.basename(file, path.extname(file)), level: 1, page: first });
  }
  if (options.bookmarks) setOutline(out, outline);
  out.setCreator("OpenNeko");
  return { doc: out, renamed };
}

function splitInput(input) {
  if (fs.existsSync(input)) return { file: input };
  const match = /^(.*\.pdf):([\d,\s\-last]+)$/i.exec(input);
  if (match && fs.existsSync(match[1])) return { file: match[1], pages: match[2] };
  fail(`no such file: ${input}`);
}

async function split(file, outDir, options = {}) {
  const source = await loadForWrite(file, options);
  const count = source.getPageCount();
  let groups;
  if (options.ranges) {
    groups = String(options.ranges).split(/\s*;\s*|\s+/).filter(Boolean).map((r) => parseRanges(r, count));
  } else {
    const every = Number(options.every ?? 1);
    if (!(Number.isInteger(every) && every >= 1)) fail("--every must be a whole number of pages");
    groups = [];
    for (let p = 1; p <= count; p += every) groups.push(Array.from({ length: Math.min(every, count - p + 1) }, (_, i) => p + i));
  }
  fs.mkdirSync(outDir, { recursive: true });
  const base = path.basename(file, path.extname(file));
  const files = [];
  for (const group of groups) {
    const doc = await PDFDocument.create();
    await copyInto(doc, source, group.map((n) => n - 1));
    const title = source.getTitle();
    if (title) doc.setTitle(title);
    const label = group.length === 1 ? `${group[0]}` : `${group[0]}-${group[group.length - 1]}`;
    const name = path.join(outDir, `${base}-p${label}.pdf`);
    doc.setModificationDate(new Date());
    fs.writeFileSync(name, await doc.save());
    files.push({ file: name, pages: label });
  }
  return { files };
}

function pagesOf(doc, op) {
  return parseRanges(op.pages ?? op.page, doc.getPageCount()).map((n) => n - 1);
}

async function fontFor(doc, cache, op) {
  const key = JSON.stringify(op.font ?? "Helvetica");
  if (!cache.has(key)) cache.set(key, await family(doc, op.font));
  const font = pick(cache.get(key), op);
  await cover(doc, font, JSON.stringify(op));
  return font;
}

function place(page, op, width, height) {
  const box = page.getCropBox();
  const top = box.y + box.height;
  if (op.position) {
    if (!POSITIONS.includes(op.position)) fail(`position must be one of ${POSITIONS.join(", ")}`);
    const margin = cm(op.margin ?? 1.2);
    const [v, h] = op.position.split("-");
    const x = h === "left" ? box.x + margin : h === "right" ? box.x + box.width - margin - width : box.x + (box.width - width) / 2;
    const y = v === "top" ? top - margin - height : box.y + margin;
    return { x, y };
  }
  if (op.x === undefined || op.y === undefined) fail(`${op.op} needs x and y (cm from the top-left corner) or a position`);
  return { x: box.x + cm(op.x), y: top - cm(op.y) - height };
}

function opacity(op, fallback) {
  const value = op.opacity ?? fallback;
  if (!(value > 0 && value <= 1)) fail("opacity must be more than 0 and at most 1");
  return value;
}

async function applyOp(doc, op, state) {
  if (!op || typeof op !== "object" || typeof op.op !== "string") fail(`each operation needs "op", got ${JSON.stringify(op)?.slice(0, 80)}`);
  const count = doc.getPageCount();
  switch (op.op) {
    case "rotate": {
      const turn = Number(op.degrees ?? 90);
      if (![90, 180, 270, -90].includes(turn)) fail("rotate degrees must be 90, 180, 270 or -90");
      for (const i of pagesOf(doc, op)) {
        const page = doc.getPage(i);
        page.setRotation(degrees((((page.getRotation().angle + turn) % 360) + 360) % 360));
      }
      return;
    }
    case "delete": {
      const gone = pagesOf(doc, op);
      if (gone.length >= count) fail("delete would remove every page; a PDF needs at least one page");
      for (const i of [...new Set(gone)].sort((a, b) => b - a)) removePage(doc, i);
      pruneFields(doc);
      return;
    }
    case "keep": {
      const keep = new Set(pagesOf(doc, op));
      for (let i = count - 1; i >= 0; i--) if (!keep.has(i)) removePage(doc, i);
      pruneFields(doc);
      return;
    }
    case "reorder": {
      const order = parseRanges(op.order, count, "order").map((n) => n - 1);
      if (order.length !== count || new Set(order).size !== count) fail(`order must name each of the ${count} pages once, for example "3,1-2,4-"`);
      const pages = doc.getPages();
      for (let i = count - 1; i >= 0; i--) removePage(doc, i);
      order.forEach((from, i) => doc.insertPage(i, pages[from]));
      return;
    }
    case "move": {
      const moving = pagesOf(doc, op);
      const to = op.to === "end" ? count + 1 : op.to === "start" ? 1 : Number(op.to);
      if (!(Number.isInteger(to) && to >= 1 && to <= count + 1)) fail(`move needs "to": a page position 1 to ${count + 1}, "start" or "end"`);
      const pages = doc.getPages();
      const rest = pages.map((p, i) => i).filter((i) => !moving.includes(i));
      const before = rest.filter((i) => i < to - 1);
      const order = [...before, ...moving, ...rest.filter((i) => i >= to - 1)];
      for (let i = count - 1; i >= 0; i--) removePage(doc, i);
      order.forEach((from, i) => doc.insertPage(i, pages[from]));
      return;
    }
    case "insert": {
      const at = op.at === "start" ? 0 : op.after !== undefined ? Number(op.after) : count;
      if (!(Number.isInteger(at) && at >= 0 && at <= count)) fail(`insert needs "after": a page number 0 to ${count}, or "at": "start"`);
      if (op.blank) {
        const size = op.size ? pageSize({ size: op.size, orientation: op.orientation }) : doc.getPage(Math.max(0, at - 1)).getSize();
        const n = op.count ?? 1;
        for (let k = 0; k < n; k++) doc.insertPage(at + k, Array.isArray(size) ? size : [size.width, size.height]);
        return;
      }
      if (!op.file) fail('insert needs "file" (a PDF) or "blank": true');
      const source = await loadForWrite(op.file, state.options);
      const indices = parseRanges(op.pages, source.getPageCount(), `pages of ${path.basename(op.file)}`).map((n) => n - 1);
      const { renamed } = await copyInto(doc, source, indices, at);
      state.notes.push(...renamed.map((r) => `field "${r.from}" from ${path.basename(op.file)} is now "${r.to}"`));
      return;
    }
    case "text": {
      if (op.text === undefined) fail('text needs "text"');
      const font = await fontFor(doc, state.fonts, op);
      const size = op.size ?? 11;
      for (const i of pagesOf(doc, op)) {
        const page = doc.getPage(i);
        const lines = clean(String(op.text).replace(/\{PAGE\}/g, String(i + 1)).replace(/\{PAGES\}/g, String(count)), font).split("\n");
        const width = Math.max(...lines.map((l) => measure(l, font, size)));
        const lh = size * 1.25;
        const { x, y } = place(page, op, width, lines.length * lh);
        if (op.background) page.drawRectangle({ x: x - 2, y: y - 2, width: width + 4, height: lines.length * lh + 4, color: color(op.background) });
        lines.forEach((line, k) => {
          const w = measure(line, font, size);
          const lx = op.align === "right" ? x + width - w : op.align === "center" ? x + (width - w) / 2 : x;
          draw(page, line, { x: lx, y: y + (lines.length - k - 1) * lh + size * 0.25, font, size, color: color(op.color), opacity: opacity(op, 1) });
        });
      }
      return;
    }
    case "image": {
      if (!op.image || !fs.existsSync(op.image)) fail(`image needs "image": a PNG or JPEG path that exists`);
      const bytes = fs.readFileSync(op.image);
      const image = bytes[0] === 0x89 ? await doc.embedPng(bytes) : bytes[0] === 0xff ? await doc.embedJpg(bytes) : fail(`${path.basename(op.image)} must be a PNG or JPEG image`);
      const width = op.width ? cm(op.width) : op.height ? (image.width * cm(op.height)) / image.height : image.width * 0.75;
      const height = op.height && op.width ? cm(op.height) : (image.height * width) / image.width;
      for (const i of pagesOf(doc, op)) {
        const page = doc.getPage(i);
        const { x, y } = place(page, op, width, height);
        page.drawImage(image, { x, y, width, height, opacity: opacity(op, 1) });
      }
      return;
    }
    case "watermark": {
      if (!op.text) fail('watermark needs "text"');
      const font = await fontFor(doc, state.fonts, { ...op, bold: op.bold ?? true });
      const text = clean(op.text, font);
      const angle = op.angle ?? 45;
      for (const i of pagesOf(doc, op)) {
        const page = doc.getPage(i);
        const box = page.getCropBox();
        const fit = (Math.hypot(box.width, box.height) * 0.7) / measure(text, font, 1);
        const size = op.size ?? Math.min(fit, 96);
        const width = measure(text, font, size);
        const rad = (angle * Math.PI) / 180;
        const cx = box.x + box.width / 2;
        const cy = box.y + box.height / 2;
        const x = cx - (Math.cos(rad) * width) / 2 + (Math.sin(rad) * size * 0.35);
        const y = cy - (Math.sin(rad) * width) / 2 - (Math.cos(rad) * size * 0.35);
        draw(page, text, { x, y, font, size, color: color(op.color ?? "#C00000"), opacity: opacity(op, 0.15), angle });
      }
      return;
    }
    case "pageNumbers": {
      const format = op.format ?? "{PAGE} / {PAGES}";
      const font = await fontFor(doc, state.fonts, op);
      const size = op.size ?? 9;
      const start = op.start ?? 1;
      const selected = pagesOf(doc, op);
      const total = op.total ?? selected.length + start - 1;
      selected.forEach((i, k) => {
        const page = doc.getPage(i);
        const text = clean(format.replace(/\{PAGE\}/g, String(start + k)).replace(/\{PAGES\}/g, String(total)), font);
        const width = measure(text, font, size);
        const { x, y } = place(page, { ...op, position: op.position ?? "bottom-center", margin: op.margin ?? 1 }, width, size);
        draw(page, text, { x, y: y + size * 0.2, font, size, color: color(op.color ?? "#404040") });
      });
      return;
    }
    case "crop": {
      for (const i of pagesOf(doc, op)) {
        const page = doc.getPage(i);
        const box = page.getCropBox();
        const [t, r, b, l] = ["top", "right", "bottom", "left"].map((k) => cm(op[k] ?? 0));
        if (box.width - l - r < 36 || box.height - t - b < 36) fail("crop leaves less than half an inch of the page");
        page.setCropBox(box.x + l, box.y + b, box.width - l - r, box.height - t - b);
      }
      return;
    }
    case "resize": {
      const [w, h] = pageSize({ size: op.size, orientation: op.orientation });
      for (const i of pagesOf(doc, op)) {
        const page = doc.getPage(i);
        const { width, height } = page.getSize();
        const scale = Math.min(w / width, h / height);
        page.scaleContent(scale, scale);
        page.scaleAnnotations(scale, scale);
        page.setSize(w, h);
        page.translateContent((w - width * scale) / 2, (h - height * scale) / 2);
      }
      return;
    }
    case "properties": {
      setProperties(doc, op);
      return;
    }
    case "fill": {
      const filled = fill(doc, op.values, op);
      if (doc.getForm().hasXFA()) {
        doc.getForm().deleteXFA();
        state.notes.push("the form had an XFA copy; it is removed so that viewers show the filled fields");
      }
      await updateAppearances(doc, state, op);
      state.notes.push(`filled ${filled.length} field${filled.length === 1 ? "" : "s"}`);
      return;
    }
    case "flatten": {
      await updateAppearances(doc, state, op);
      doc.getForm().flatten();
      return;
    }
    case "redact":
      fail("this skill cannot redact. A box drawn over text leaves the text in the file, where anyone can copy it. Tell the user to use a redaction tool");
      break;
    default:
      fail(`unknown op "${op.op}"; ops are rotate, delete, keep, reorder, move, insert, text, image, watermark, pageNumbers, crop, resize, properties, fill, flatten`);
  }
}

async function updateAppearances(doc, state, op) {
  const font = op.font ? await fontFor(doc, state.fonts, op) : await doc.embedFont(StandardFonts.Helvetica);
  for (const field of doc.getForm().getFields()) {
    if (!field.needsAppearancesUpdate()) continue;
    try {
      field.defaultUpdateAppearances(font);
    } catch (error) {
      if (!/cannot encode|WinAnsi/i.test(error.message)) throw error;
      const fallback = op.font ? null : await fallbackFor(doc, fieldText(field));
      if (!fallback) fail(`field "${field.getName()}" has a character that ${op.font ? "the font" : "Helvetica and the sandbox image's fonts"} cannot draw; give "font": a .ttf or .otf path that has it`);
      field.defaultUpdateAppearances(fallback);
    }
  }
}

function fieldText(field) {
  if (typeof field.getText === "function") return field.getText() ?? "";
  if (typeof field.getOptions === "function") return field.getOptions().join("");
  return "";
}

async function edit(file, ops, options = {}) {
  const list = Array.isArray(ops) ? ops : ops?.ops;
  if (!Array.isArray(list) || !list.length) fail('edit needs a list of ops, or { "ops": [...] }');
  const doc = await loadForWrite(file, options);
  const state = { fonts: new Map(), notes: [], options };
  for (let k = 0; k < list.length; k++) {
    try {
      await applyOp(doc, list[k], state);
    } catch (error) {
      if (error.constructor.name === "UserError") error.message = `op ${k + 1} (${list[k]?.op}): ${error.message}`;
      throw error;
    }
  }
  return { doc, notes: state.notes };
}

async function fillFile(file, values, options = {}) {
  const doc = await loadForWrite(file, options);
  const state = { fonts: new Map(), notes: [], options };
  const fields = values?.values && typeof values.values === "object" ? values.values : values;
  await applyOp(doc, { op: "fill", values: fields, force: values?.force, font: options.font }, state);
  if (options.flatten) await applyOp(doc, { op: "flatten", font: options.font }, state);
  return { doc, notes: state.notes };
}

module.exports = { edit, fillFile, merge, split };
