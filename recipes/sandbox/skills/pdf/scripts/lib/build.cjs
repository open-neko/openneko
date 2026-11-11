"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { PDFDocument, PDFName, PDFString, StandardFonts, rgb } = require("pdf-lib");
const { cm, fail, pageSize } = require("./common.cjs");
const { clean, cover, draw, family, mark, measure, pick, setOutline } = require("./fonts.cjs");

const HEADING_SIZES = [0, 18, 15, 13, 12];
const BULLETS = ["•", "–", "·"];

function color(value, fallback = [0, 0, 0]) {
  if (value === undefined || value === null) return rgb(...fallback);
  const match = /^#?([0-9a-f]{6})$/i.exec(String(value));
  if (!match) fail(`color "${value}" must be a hex value such as #C00000`);
  const n = parseInt(match[1], 16);
  return rgb(((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255);
}

function parseInline(text) {
  const runs = [];
  const pattern = /(\*\*|__)(.+?)\1|(\*|_)(?!\s)(.+?)(?<!\s)\3|`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)|~~(.+?)~~/g;
  let last = 0;
  for (const m of text.matchAll(pattern)) {
    if (m.index > last) runs.push({ text: text.slice(last, m.index) });
    if (m[2] !== undefined) runs.push(...parseInline(m[2]).map((r) => ({ ...r, bold: true })));
    else if (m[4] !== undefined) runs.push(...parseInline(m[4]).map((r) => ({ ...r, italic: true })));
    else if (m[5] !== undefined) runs.push({ text: m[5], code: true });
    else if (m[6] !== undefined) runs.push({ text: m[6], link: m[7] });
    else if (m[8] !== undefined) runs.push(...parseInline(m[8]));
    last = m.index + m[0].length;
  }
  if (last < text.length) runs.push({ text: text.slice(last) });
  return runs.filter((r) => r.text !== "");
}

function markdownBlocks(source) {
  const lines = String(source).replace(/\r\n?/g, "\n").split("\n");
  const blocks = [];
  let para = [];
  const flush = () => {
    if (para.length) blocks.push({ paragraph: parseInline(para.join(" ")) });
    para = [];
  };
  const cells = (line) => line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((c) => parseInline(c.trim()));
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    let m;
    if (!line.trim()) {
      flush();
    } else if (/^\s*```/.test(line)) {
      flush();
      const code = [];
      while (++i < lines.length && !/^\s*```/.test(lines[i])) code.push(lines[i]);
      blocks.push({ code: code.join("\n") });
    } else if ((m = /^(#{1,6})\s+(.*?)\s*#*$/.exec(line))) {
      flush();
      blocks.push({ heading: parseInline(m[2]), level: Math.min(m[1].length, 4) });
    } else if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {
      flush();
      blocks.push({ rule: true });
    } else if (/^\s*\|/.test(line) && i + 1 < lines.length && /^\s*\|?\s*:?-{3,}/.test(lines[i + 1])) {
      flush();
      const rows = [cells(line)];
      const aligns = cells(lines[i + 1]).map((c) => {
        const t = c.map((r) => r.text).join("");
        return t.endsWith(":") ? (t.startsWith(":") ? "center" : "right") : "left";
      });
      i++;
      while (i + 1 < lines.length && /^\s*\|/.test(lines[i + 1])) rows.push(cells(lines[++i]));
      blocks.push({ table: rows, align: aligns });
    } else if (/^\s*>/.test(line)) {
      flush();
      const quote = [line.replace(/^\s*>\s?/, "")];
      while (i + 1 < lines.length && /^\s*>/.test(lines[i + 1])) quote.push(lines[++i].replace(/^\s*>\s?/, ""));
      blocks.push({ quote: parseInline(quote.join(" ")) });
    } else if (/^\s*([-*+]|\d+[.)])\s+/.test(line)) {
      flush();
      const items = [];
      while (i < lines.length && /^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) {
        const [, indent, marker, text] = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(lines[i]);
        if (items.length && !indent && /\d/.test(marker) !== items[0].numbered) break;
        items.push({ depth: Math.floor(indent.replace(/\t/g, "    ").length / 2), numbered: /\d/.test(marker), text: parseInline(text) });
        i++;
      }
      i--;
      blocks.push(nestList(items));
    } else {
      para.push(line.trim());
    }
  }
  flush();
  return blocks;
}

function nestList(flat) {
  const root = { items: [], numbered: flat[0].numbered };
  const stack = [{ depth: -1, node: root }];
  for (const entry of flat) {
    while (stack.length > 1 && stack[stack.length - 1].depth >= entry.depth) stack.pop();
    const parent = stack[stack.length - 1].node;
    if (!parent.items) {
      parent.items = [];
      parent.numbered = entry.numbered;
    }
    const node = { text: entry.text };
    parent.items.push(node);
    stack.push({ depth: entry.depth, node });
  }
  const convert = (node) => node.items.map((item) => {
    if (!item.items) return item.text;
    return { text: item.text, [item.numbered ? "numbered" : "bullets"]: convert(item) };
  });
  return { [root.numbered ? "numbered" : "bullets"]: convert(root) };
}

function toRuns(value) {
  if (value === undefined || value === null) return [];
  if (typeof value === "string" || typeof value === "number") return [{ text: String(value) }];
  if (Array.isArray(value)) return value.flatMap((r) => (typeof r === "string" || typeof r === "number" ? [{ text: String(r) }] : [r]));
  if (typeof value === "object" && "text" in value) return [value];
  fail(`expected text or runs, got ${JSON.stringify(value).slice(0, 80)}`);
}

class Layout {
  constructor(doc, fonts, mono, spec) {
    this.doc = doc;
    this.fonts = fonts;
    this.mono = mono;
    [this.width, this.height] = pageSize(spec.page);
    const m = spec.page?.margins ?? 2;
    const margins = typeof m === "number" ? { top: m, right: m, bottom: m, left: m } : { top: 2, right: 2, bottom: 2, left: 2, ...m };
    this.margin = Object.fromEntries(Object.entries(margins).map(([k, v]) => [k, cm(v)]));
    this.size = spec.font?.size ?? 11;
    this.color = spec.font?.color;
    this.lineSpacing = spec.lineSpacing ?? 1.3;
    this.contentWidth = this.width - this.margin.left - this.margin.right;
    if (this.contentWidth < 72) fail("the margins leave less than 2.5 cm for text");
    this.outline = [];
    this.links = [];
    this.newPage();
  }

  newPage() {
    this.page = this.doc.addPage([this.width, this.height]);
    this.y = this.height - this.margin.top;
    this.atTop = true;
  }

  room() {
    return this.y - this.margin.bottom;
  }

  ensure(height) {
    if (height > this.room() && !this.atTop) this.newPage();
  }

  space(points) {
    if (this.atTop) return;
    if (points >= this.room()) this.newPage();
    else this.y -= points;
  }

  segments(runs, base) {
    const out = [];
    for (const run of runs) {
      const style = { ...base, ...run };
      const font = style.code ? this.mono : pick(this.fonts, style);
      const size = style.size ?? base.size;
      const text = clean(run.text ?? "", font);
      for (const piece of text.split(/(\n|[  ]+)/)) {
        if (piece === "") continue;
        out.push({
          text: piece,
          font,
          size,
          color: style.color ?? this.color,
          link: style.link,
          underline: style.underline ?? Boolean(style.link),
          kind: piece === "\n" ? "break" : /^[  ]+$/.test(piece) && !piece.includes(" ") ? "space" : "word",
          width: piece === "\n" ? 0 : measure(piece, font, size),
        });
      }
    }
    return out;
  }

  lines(runs, maxWidth, base) {
    const lines = [];
    let line = { segs: [], width: 0 };
    let pendingSpace = null;
    const push = () => {
      lines.push(line);
      line = { segs: [], width: 0 };
      pendingSpace = null;
    };
    for (const seg of this.segments(runs, base)) {
      if (seg.kind === "break") {
        push();
        continue;
      }
      if (seg.kind === "space") {
        if (line.segs.length) pendingSpace = seg;
        continue;
      }
      const spaceWidth = pendingSpace ? pendingSpace.width : 0;
      if (line.segs.length && line.width + spaceWidth + seg.width > maxWidth) push();
      if (pendingSpace && line.segs.length) {
        line.segs.push(pendingSpace);
        line.width += pendingSpace.width;
      }
      pendingSpace = null;
      if (seg.width > maxWidth) {
        let chunk = "";
        for (const ch of seg.text) {
          const w = measure(chunk + ch, seg.font, seg.size);
          if (w > maxWidth - line.width && (chunk || line.segs.length)) {
            if (chunk) line.segs.push({ ...seg, text: chunk, width: measure(chunk, seg.font, seg.size) });
            push();
            chunk = ch;
          } else chunk += ch;
        }
        if (chunk) {
          const width = measure(chunk, seg.font, seg.size);
          line.segs.push({ ...seg, text: chunk, width });
          line.width += width;
        }
        continue;
      }
      line.segs.push(seg);
      line.width += seg.width;
    }
    if (line.segs.length || !lines.length) push();
    for (const l of lines) l.height = Math.max(base.size, ...l.segs.map((s) => s.size)) * this.lineSpacing;
    return lines;
  }

  drawLine(line, x, top, maxWidth, align, page = this.page) {
    const size = Math.max(...line.segs.map((s) => s.size), 1);
    const baseline = top - line.height + (line.height - size) / 2 + size * 0.22;
    let cx = x + (align === "center" ? (maxWidth - line.width) / 2 : align === "right" ? maxWidth - line.width : 0);
    for (const seg of line.segs) {
      if (seg.kind === "word") {
        draw(page, seg.text, { x: cx, y: baseline, font: seg.font, size: seg.size, color: color(seg.color) });
      }
      const k = line.segs.indexOf(seg);
      const inside = seg.kind === "word" || (k > 0 && k < line.segs.length - 1 && line.segs[k - 1].link === seg.link && line.segs[k + 1].link === seg.link);
      if (seg.underline && inside) {
        page.drawLine({ start: { x: cx, y: baseline - seg.size * 0.12 }, end: { x: cx + seg.width, y: baseline - seg.size * 0.12 }, thickness: Math.max(seg.size / 18, 0.5), color: color(seg.color) });
      }
      if (seg.link && inside) this.links.push({ page, url: seg.link, rect: [cx, baseline - seg.size * 0.25, cx + seg.width, baseline + seg.size * 0.85] });
      cx += seg.width;
    }
  }

  text(runs, options = {}) {
    const base = { size: options.size ?? this.size, bold: options.bold, italic: options.italic, color: options.color };
    const indent = options.indent ?? 0;
    const width = this.contentWidth - indent - (options.rightIndent ?? 0);
    const lines = this.lines(runs, width, base);
    const keep = options.keepWithNext ?? 0;
    const firstLines = lines.slice(0, 2).reduce((h, l) => h + l.height, 0);
    this.ensure(firstLines + keep);
    const start = { page: this.page, y: this.y };
    lines.forEach((line, i) => {
      if (line.height > this.room()) this.newPage();
      if (options.marker && i === 0) {
        const markerRuns = this.lines([{ text: options.marker }], indent, base)[0];
        this.drawLine(markerRuns, this.margin.left + indent - options.markerWidth, this.y, options.markerWidth, "left");
      }
      this.drawLine(line, this.margin.left + indent, this.y, width, options.align ?? "left");
      this.y -= line.height;
      this.atTop = false;
    });
    return start;
  }

  paragraph(block) {
    const runs = toRuns(block.paragraph ?? block);
    this.space((block.spaceBefore ?? 0));
    this.text(runs, { align: block.align, size: block.size, bold: block.bold, italic: block.italic, color: block.color, indent: cm(block.indent ?? 0) });
    this.space(block.spaceAfter ?? this.size * 0.6);
  }

  heading(block, level) {
    const size = level === 0 ? 24 : HEADING_SIZES[level] ?? 12;
    this.space(level === 0 ? 0 : size * 0.8);
    const runs = toRuns(level === 0 ? block.title : block.heading);
    const start = this.text(runs, { size, bold: true, color: block.color ?? (level === 0 ? undefined : "#1F3864"), align: block.align, keepWithNext: this.size * this.lineSpacing * 2 });
    if (level > 0 && block.bookmark !== false) {
      this.outline.push({ title: runs.map((r) => r.text).join("").trim(), level, page: this.doc.getPages().indexOf(start.page), y: start.y });
    }
    this.space(level === 0 ? size * 0.5 : size * 0.4);
  }

  list(items, numbered, depth) {
    if (!Array.isArray(items)) fail("bullets and numbered take a list of items");
    if (depth > 5) fail("lists nest at most six levels deep");
    const indent = cm(0.65 * (depth + 1));
    items.forEach((item, i) => {
      const isObject = item && typeof item === "object" && !Array.isArray(item);
      const runs = toRuns(isObject ? item.text ?? "" : item);
      const marker = numbered ? `${listNumber(i + 1, depth)}.` : BULLETS[depth % BULLETS.length];
      this.text(runs, { indent, marker, markerWidth: cm(0.55) });
      this.space(this.size * 0.2);
      if (isObject) {
        if (item.items) this.list(item.items, numbered, depth + 1);
        if (item.bullets) this.list(item.bullets, false, depth + 1);
        if (item.numbered) this.list(item.numbered, true, depth + 1);
      }
    });
    if (depth === 0) this.space(this.size * 0.4);
  }

  quote(block) {
    const startPage = this.page;
    const top = this.y;
    this.text(toRuns(block.quote), { italic: true, indent: cm(0.6), color: block.color ?? "#404040" });
    if (this.page === startPage) {
      this.page.drawRectangle({ x: this.margin.left + cm(0.15), y: this.y, width: 2, height: top - this.y, color: color("#A0A0A0") });
    }
    this.space(this.size * 0.6);
  }

  code(block) {
    const size = block.size ?? this.size * 0.9;
    const lines = String(block.code).split("\n");
    const lh = size * 1.25;
    const pad = 4;
    for (let i = 0; i < lines.length; ) {
      this.ensure(lh + pad * 2);
      const fit = Math.max(1, Math.floor((this.room() - pad * 2) / lh));
      const chunk = lines.slice(i, i + fit);
      const height = chunk.length * lh + pad * 2;
      this.page.drawRectangle({ x: this.margin.left, y: this.y - height, width: this.contentWidth, height, color: color("#F2F2F2") });
      chunk.forEach((text, k) => {
        const clipped = clean(text, this.mono);
        let shown = clipped;
        while (shown && measure(shown, this.mono, size) > this.contentWidth - pad * 2) shown = shown.slice(0, -1);
        if (shown) draw(this.page, shown, { x: this.margin.left + pad, y: this.y - pad - (k + 1) * lh + size * 0.3, font: this.mono, size, color: color(undefined) });
      });
      this.y -= height;
      this.atTop = false;
      i += chunk.length;
      if (i < lines.length) this.newPage();
    }
    this.space(this.size * 0.6);
  }

  rule() {
    this.space(this.size * 0.4);
    this.page.drawLine({ start: { x: this.margin.left, y: this.y }, end: { x: this.margin.left + this.contentWidth, y: this.y }, thickness: 0.75, color: color("#A0A0A0") });
    this.space(this.size * 0.8);
  }

  async image(block) {
    const file = block.image;
    if (!fs.existsSync(file)) fail(`no such image: ${file}`);
    const bytes = fs.readFileSync(file);
    let image;
    if (bytes[0] === 0x89 && bytes[1] === 0x50) image = await this.doc.embedPng(bytes);
    else if (bytes[0] === 0xff && bytes[1] === 0xd8) image = await this.doc.embedJpg(bytes);
    else fail(`${path.basename(file)} must be a PNG or JPEG image`);
    let width = block.width ? cm(block.width) : block.height ? (image.width * cm(block.height)) / image.height : Math.min(this.contentWidth, image.width * 0.75);
    let height = block.height && block.width ? cm(block.height) : (image.height * width) / image.width;
    const maxHeight = this.height - this.margin.top - this.margin.bottom - (block.caption ? this.size * 2 : 0);
    const scale = Math.min(1, this.contentWidth / width, maxHeight / height);
    width *= scale;
    height *= scale;
    this.ensure(height + (block.caption ? this.size * 2 : 0));
    const x = this.margin.left + (block.align === "center" ? (this.contentWidth - width) / 2 : block.align === "right" ? this.contentWidth - width : 0);
    this.page.drawImage(image, { x, y: this.y - height, width, height });
    this.y -= height;
    this.atTop = false;
    if (block.caption) {
      this.space(this.size * 0.3);
      this.text(toRuns(block.caption), { italic: true, size: this.size * 0.9, align: block.align ?? "left" });
    }
    this.space(this.size * 0.6);
  }

  table(block) {
    const rows = block.table;
    if (!Array.isArray(rows) || !rows.length || !rows.every(Array.isArray)) fail("table takes a list of rows, and each row is a list of cells");
    const size = block.size ?? this.size - 1;
    const pad = 4;
    const cellOf = (cell) => (cell && typeof cell === "object" && !Array.isArray(cell) && ("text" in cell || "runs" in cell) ? cell : { text: cell });
    const grid = rows.map((row) => row.map(cellOf));
    const columns = Math.max(...grid.map((row) => row.reduce((n, c) => n + (c.colspan ?? 1), 0)));
    const header = block.header !== false && grid.length > 1;
    const alignOf = (c, col) => c.align ?? (Array.isArray(block.align) ? block.align[col] : block.align) ?? (typeof c.text === "number" ? "right" : "left");
    const runsOf = (c, isHeader) => toRuns(c.runs ?? (c.text === undefined || c.text === null ? "" : typeof c.text === "number" ? String(c.text) : c.text)).map((r) => ({ ...r, bold: r.bold ?? c.bold ?? (isHeader || undefined) }));
    let widths;
    if (block.widths) {
      if (!Array.isArray(block.widths) || block.widths.length !== columns) fail(`table widths needs ${columns} values in cm`);
      widths = block.widths.map(cm);
      const total = widths.reduce((a, b) => a + b, 0);
      if (total > this.contentWidth) widths = widths.map((w) => (w * this.contentWidth) / total);
    } else {
      const desired = Array(columns).fill(pad * 2 + 10);
      const minimum = Array(columns).fill(pad * 2 + 10);
      grid.forEach((row, r) => {
        let col = 0;
        for (const c of row) {
          if ((c.colspan ?? 1) === 1) {
            const segs = this.segments(runsOf(c, header && r === 0), { size });
            const full = segs.filter((s) => s.kind !== "break").reduce((w, s) => w + s.width, 0);
            const word = Math.max(0, ...segs.filter((s) => s.kind === "word").map((s) => s.width));
            desired[col] = Math.max(desired[col], Math.min(full, this.contentWidth * 0.6) + pad * 2);
            minimum[col] = Math.max(minimum[col], Math.min(word, this.contentWidth / columns) + pad * 2);
          }
          col += c.colspan ?? 1;
        }
      });
      const want = desired.reduce((a, b) => a + b, 0);
      widths = desired.map((d) => (d * this.contentWidth) / want);
      const short = widths.map((w, i) => Math.max(0, minimum[i] - w));
      const deficit = short.reduce((a, b) => a + b, 0);
      if (deficit > 0) {
        const spare = widths.map((w, i) => (short[i] > 0 ? 0 : w - minimum[i]));
        const spareTotal = spare.reduce((a, b) => a + b, 0);
        if (spareTotal > deficit) widths = widths.map((w, i) => (short[i] > 0 ? minimum[i] : w - (spare[i] * deficit) / spareTotal));
      }
    }
    const layoutRow = (row, r) => {
      let col = 0;
      const cells = row.map((c) => {
        const span = c.colspan ?? 1;
        const width = widths.slice(col, col + span).reduce((a, b) => a + b, 0);
        if (col + span > columns) fail(`table row ${r + 1} has more cells than columns`);
        const x = this.margin.left + widths.slice(0, col).reduce((a, b) => a + b, 0);
        const isHeader = header && r === 0;
        const lines = this.lines(runsOf(c, isHeader), width - pad * 2, { size });
        col += span;
        return { c, x, width, lines, align: alignOf(c, col - span), fill: c.fill ?? (isHeader ? block.headerFill ?? "#E7E9EE" : r % 2 === 0 && block.stripes ? "#F5F6F8" : undefined) };
      });
      const height = Math.max(...cells.map((cell) => cell.lines.reduce((h, l) => h + l.height, 0))) + pad * 2;
      return { cells, height, filledTo: col };
    };
    const laid = grid.map(layoutRow);
    const fullHeight = this.height - this.margin.top - this.margin.bottom;
    const drawRow = (row) => {
      const top = this.y;
      for (const cell of row.cells) {
        if (cell.fill) this.page.drawRectangle({ x: cell.x, y: top - row.height, width: cell.width, height: row.height, color: color(cell.fill) });
        let y = top - pad;
        for (const line of cell.lines) {
          this.drawLine(line, cell.x + pad, y, cell.width - pad * 2, cell.align);
          y -= line.height;
        }
        this.page.drawRectangle({ x: cell.x, y: top - row.height, width: cell.width, height: row.height, borderColor: color(block.borderColor ?? "#9AA0A6"), borderWidth: 0.5 });
      }
      this.y -= row.height;
      this.atTop = false;
    };
    this.space(this.size * 0.2);
    laid.forEach((row, r) => {
      if (row.height > fullHeight - (header ? laid[0].height : 0)) fail(`table row ${r + 1} is taller than a page; shorten its text or make the column wider`);
      if (r === 0 && header && laid.length > 1) this.ensure(row.height + laid[1].height);
      else if (row.height > this.room()) {
        this.newPage();
        if (header && r > 0 && block.repeatHeader !== false) drawRow(laid[0]);
      }
      drawRow(row);
    });
    if (block.caption) {
      this.space(this.size * 0.3);
      this.text(toRuns(block.caption), { italic: true, size: this.size * 0.9 });
    }
    this.space(this.size * 0.8);
  }

  async block(block) {
    if (typeof block === "string" || typeof block === "number") return this.paragraph({ paragraph: String(block) });
    if (!block || typeof block !== "object" || Array.isArray(block)) fail(`a block must be text or an object, got ${JSON.stringify(block)?.slice(0, 80)}`);
    if ("paragraph" in block) return this.paragraph(block);
    if ("heading" in block) {
      const level = block.level ?? 1;
      if (!(level >= 1 && level <= 4)) fail("heading level must be 1 to 4");
      return this.heading(block, level);
    }
    if ("title" in block) return this.heading(block, 0);
    if ("subtitle" in block) {
      this.text(toRuns(block.subtitle), { size: 14, color: block.color ?? "#595959", align: block.align });
      return this.space(this.size);
    }
    if ("bullets" in block) return this.list(block.bullets, false, 0);
    if ("numbered" in block) return this.list(block.numbered, true, 0);
    if ("table" in block) return this.table(block);
    if ("image" in block) return this.image(block);
    if ("quote" in block) return this.quote(block);
    if ("code" in block) return this.code(block);
    if ("rule" in block) return this.rule();
    if ("spacer" in block) return this.space(cm(block.spacer));
    if ("pageBreak" in block) return this.atTop ? undefined : this.newPage();
    if ("markdown" in block) {
      for (const b of markdownBlocks(block.markdown)) await this.block(b);
      return;
    }
    fail(`unknown block with keys ${Object.keys(block).join(", ")}; see SKILL.md for the block list`);
  }
}

function listNumber(n, depth) {
  if (depth % 3 === 1) {
    let s = "";
    for (let k = n; k > 0; k = Math.floor((k - 1) / 26)) s = String.fromCharCode(97 + ((k - 1) % 26)) + s;
    return s;
  }
  if (depth % 3 === 2) {
    const table = [[10, "x"], [9, "ix"], [5, "v"], [4, "iv"], [1, "i"]];
    let s = "";
    for (const [v, r] of table) while (n >= v) { s += r; n -= v; }
    return s;
  }
  return String(n);
}

function decorate(layout, spec) {
  const pages = layout.doc.getPages();
  const draw = (value, top) => {
    if (!value) return;
    const opts = typeof value === "string" ? { text: value } : value;
    pages.forEach((page, i) => {
      const text = String(opts.text).replace(/\{PAGE\}/g, String(i + 1)).replace(/\{PAGES\}/g, String(pages.length));
      const size = opts.size ?? 9;
      const lines = layout.lines([{ text }], layout.contentWidth, { size, color: opts.color ?? "#595959" });
      const line = lines[0];
      const y = top ? layout.height - layout.margin.top / 2 + line.height / 2 : layout.margin.bottom / 2 + line.height / 2;
      layout.drawLine(line, layout.margin.left, y, layout.contentWidth, opts.align ?? "center", page);
    });
  };
  draw(spec.header, true);
  draw(spec.footer, false);
}

function addLinks(doc, links) {
  for (const link of links) {
    if (!/^(https?:|mailto:)/i.test(link.url)) fail(`link "${link.url}" must start with http:, https: or mailto:`);
    const annot = doc.context.register(doc.context.obj({
      Type: "Annot",
      Subtype: "Link",
      Rect: link.rect,
      Border: [0, 0, 0],
      A: { Type: "Action", S: "URI", URI: PDFString.of(link.url) },
    }));
    link.page.node.addAnnot(annot);
  }
}

function setProperties(doc, props = {}) {
  if (props.title) doc.setTitle(String(props.title), { showInWindowTitleBar: true });
  if (props.author) doc.setAuthor(String(props.author));
  if (props.subject) doc.setSubject(String(props.subject));
  if (props.keywords) doc.setKeywords(Array.isArray(props.keywords) ? props.keywords.map(String) : String(props.keywords).split(/\s*,\s*/));
  if (props.creator) doc.setCreator(String(props.creator));
  if (props.language) {
    if (!/^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$/.test(props.language)) fail(`language "${props.language}" must be a tag such as en or en-GB`);
    doc.setLanguage(props.language);
  }
}

async function create(spec) {
  if (!spec || typeof spec !== "object" || !Array.isArray(spec.blocks)) fail('the spec needs "blocks": a list of blocks');
  const doc = await PDFDocument.create();
  const fonts = await family(doc, typeof spec.font === "string" ? spec.font : spec.font?.regular ? spec.font : spec.font?.name);
  const mono = mark(await doc.embedFont(StandardFonts.Courier), "mono");
  const sample = JSON.stringify(spec);
  for (const font of [fonts.regular, fonts.bold, fonts.italic, fonts.boldItalic, mono]) await cover(doc, font, sample);
  const layout = new Layout(doc, fonts, mono, spec);
  for (const block of spec.blocks) await layout.block(block);
  decorate(layout, spec);
  addLinks(doc, layout.links);
  if (spec.bookmarks !== false) setOutline(doc, layout.outline);
  setProperties(doc, { creator: "OpenNeko", language: "en", ...spec.properties });
  doc.setCreationDate(new Date());
  doc.catalog.set(PDFName.of("ViewerPreferences"), doc.context.obj({ DisplayDocTitle: true }));
  return doc;
}

module.exports = { color, create, markdownBlocks, parseInline, setProperties };
