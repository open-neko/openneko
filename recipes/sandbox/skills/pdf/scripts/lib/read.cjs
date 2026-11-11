"use strict";

const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const { PDFDocument } = require("pdf-lib");
const { fail, openForRead, parseRanges, readFile, signatureFields, sizeName, toCm } = require("./common.cjs");

const MAX_CHARS = 60000;

function pdfDate(raw) {
  const match = /^D:(\d{4})(\d{2})?(\d{2})?(\d{2})?(\d{2})?(\d{2})?/.exec(raw ?? "");
  if (!match) return raw || undefined;
  const [, y, mo = "01", d = "01", h = "00", mi = "00", s = "00"] = match;
  return `${y}-${mo}-${d}T${h}:${mi}:${s}`;
}

async function pageText(page) {
  const content = await page.getTextContent({ includeMarkedContent: false });
  let text = "";
  let prev;
  for (const item of content.items) {
    if (typeof item.str !== "string") continue;
    if (prev && !prev.hasEOL && item.str) {
      const size = Math.abs(item.transform[3]) || item.height || 10;
      const dy = Math.abs(item.transform[5] - prev.transform[5]);
      const gap = item.transform[4] - (prev.transform[4] + prev.width);
      if (dy > size * 0.5) text += "\n";
      else if (gap > size * 0.15 && !/\s$/.test(text) && !/^\s/.test(item.str)) text += " ";
    }
    text += item.str;
    if (item.hasEOL) text += "\n";
    if (item.str || item.hasEOL) prev = item;
  }
  return text.replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
}

async function pageLayout(page) {
  const content = await page.getTextContent({ includeMarkedContent: false });
  const items = content.items
    .filter((item) => typeof item.str === "string" && item.str.trim())
    .map((item) => ({ str: item.str, x: item.transform[4], y: item.transform[5], w: item.width, h: Math.abs(item.transform[3]) || item.height || 10 }));
  if (!items.length) return "";
  items.sort((a, b) => b.y - a.y || a.x - b.x);
  const lines = [];
  for (const item of items) {
    const line = lines.find((l) => Math.abs(l.y - item.y) <= Math.min(l.h, item.h) * 0.4);
    if (line) line.items.push(item);
    else lines.push({ y: item.y, h: item.h, items: [item] });
  }
  lines.sort((a, b) => b.y - a.y);
  const widths = items.map((i) => i.w / Math.max(i.str.length, 1)).filter((w) => w > 0).sort((a, b) => a - b);
  const charWidth = widths[Math.floor(widths.length / 2)] || 5;
  const out = [];
  let lastY;
  for (const line of lines) {
    if (lastY !== undefined && lastY - line.y > line.h * 2.2) out.push("");
    lastY = line.y;
    line.items.sort((a, b) => a.x - b.x);
    let row = "";
    for (const item of line.items) {
      const col = Math.round(item.x / charWidth);
      if (row.length < col) row += " ".repeat(col - row.length);
      else if (row && !row.endsWith(" ")) row += " ";
      row += item.str;
    }
    out.push(row.replace(/\s+$/, ""));
  }
  const indent = Math.min(...out.filter(Boolean).map((l) => l.match(/^ */)[0].length));
  return out.map((l) => l.slice(indent)).join("\n");
}

async function info(file, options = {}) {
  const doc = await openForRead(file, options.password);
  const meta = await doc.getMetadata().catch(() => ({ info: {} }));
  const fields = await doc.getFieldObjects().catch(() => null);
  const outline = await doc.getOutline().catch(() => null);
  const attachments = await doc.getAttachments().catch(() => null);
  const sizes = new Map();
  const rotated = [];
  const noText = [];
  let chars = 0;
  for (let n = 1; n <= doc.numPages; n++) {
    const page = await doc.getPage(n);
    const [x0, y0, x1, y1] = page.view;
    const name = sizeName(x1 - x0, y1 - y0);
    sizes.set(name, [...(sizes.get(name) ?? []), n]);
    if (page.rotate) rotated.push({ page: n, degrees: page.rotate });
    const text = await pageText(page);
    const count = text.replace(/\s/g, "").length;
    chars += count;
    if (count < 10) noText.push(n);
    page.cleanup();
  }
  const raw = readFile(file);
  let encrypted = false;
  let signed = [];
  try {
    const lib = await PDFDocument.load(raw, { ignoreEncryption: true, updateMetadata: false });
    encrypted = lib.isEncrypted;
    signed = signatureFields(lib);
  } catch {
    encrypted = Boolean(options.password);
  }
  const fieldNames = fields ? Object.keys(Object.fromEntries(fields)) : [];
  const m = meta.info ?? {};
  const result = {
    file,
    pages: doc.numPages,
    pdfVersion: m.PDFFormatVersion,
    title: m.Title || undefined,
    author: m.Author || undefined,
    subject: m.Subject || undefined,
    keywords: m.Keywords || undefined,
    creator: m.Creator || undefined,
    producer: m.Producer || undefined,
    created: pdfDate(m.CreationDate),
    modified: pdfDate(m.ModDate),
    encrypted,
    pageSizes: Object.fromEntries([...sizes].map(([name, pages]) => [name, compactRanges(pages)])),
    ...(rotated.length ? { rotated } : {}),
    textCharacters: chars,
    ...(noText.length ? { pagesWithoutText: compactRanges(noText) } : {}),
    form: fieldNames.length ? { fields: fieldNames.length, xfa: Boolean(m.IsXFAPresent) } : null,
    ...(signed.length ? { signatures: signed } : {}),
    ...(outline?.length ? { bookmarks: outline.slice(0, 40).map((o) => o.title) } : {}),
    ...(attachments ? { attachments: Object.keys(attachments) } : {}),
  };
  if (noText.length) {
    result.note = noText.length === doc.numPages
      ? "No page has a text layer. The file is likely a scan; run ocr."
      : `${noText.length === 1 ? "Page" : "Pages"} ${compactRanges(noText)} ${noText.length === 1 ? "has" : "have"} no text layer. A page like this is blank, or a scan or picture; run ocr on it to read a scan.`;
  }
  await doc.loadingTask.destroy();
  return result;
}

function compactRanges(pages) {
  const parts = [];
  for (let i = 0; i < pages.length; i++) {
    let j = i;
    while (j + 1 < pages.length && pages[j + 1] === pages[j] + 1) j++;
    parts.push(i === j ? `${pages[i]}` : `${pages[i]}-${pages[j]}`);
    i = j;
  }
  return parts.join(",");
}

function formatPages(pages, format, total, maxChars, nextHint) {
  if (format === "json") return JSON.stringify({ pages: pages.map((p) => ({ page: p.page, text: p.text, ...(p.note ? { note: p.note } : {}) })), ...(nextHint ? { next: nextHint } : {}) }, null, 2);
  const blocks = pages.map((p) => {
    const body = p.text || `[${p.note}]`;
    return format === "text" ? `--- page ${p.page} of ${total} ---\n${body}` : `## Page ${p.page}\n\n${body}`;
  });
  let out = blocks.join("\n\n");
  if (nextHint) out += `\n\n[Output stops at about ${maxChars} characters. Run read again with --pages ${nextHint} for the rest.]`;
  return out;
}

async function collect(file, options, perPage) {
  const doc = await openForRead(file, options.password);
  const wanted = parseRanges(options.pages, doc.numPages);
  const maxChars = Number(options["max-chars"] ?? MAX_CHARS);
  if (!(maxChars > 0)) fail("--max-chars must be a positive number");
  const pages = [];
  let size = 0;
  let nextHint;
  for (let i = 0; i < wanted.length; i++) {
    const n = wanted[i];
    const page = await doc.getPage(n);
    let entry = await perPage(page, n);
    page.cleanup();
    if (size + entry.text.length > maxChars) {
      if (pages.length) {
        nextHint = compactRanges(wanted.slice(i));
        break;
      }
      entry = { ...entry, text: entry.text.slice(0, maxChars), note: `page ${n} is cut at ${maxChars} characters; raise --max-chars to see all of it` };
    }
    pages.push(entry);
    size += entry.text.length;
  }
  const total = doc.numPages;
  await doc.loadingTask.destroy();
  return { pages, total, maxChars, nextHint };
}

async function read(file, options = {}) {
  const format = options.format ?? "markdown";
  if (!["markdown", "text", "json"].includes(format)) fail("--format must be markdown, text or json");
  const { pages, total, maxChars, nextHint } = await collect(file, options, async (page, n) => {
    const text = options.layout ? await pageLayout(page) : await pageText(page);
    const entry = { page: n, text };
    if (text.replace(/\s/g, "").length < 10) entry.note = `page ${n} has no text layer; if it is a scan, run ocr --pages ${n}`;
    return entry;
  });
  return formatPages(pages, format, total, maxChars, nextHint);
}

function which(binary) {
  for (const dir of (process.env.PATH ?? "").split(path.delimiter)) {
    const candidate = path.join(dir, binary);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      return candidate;
    } catch {}
  }
  return null;
}

function run(binary, args) {
  const result = spawnSync(binary, args, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
  if (result.error) fail(`${path.basename(binary)} did not run: ${result.error.message}`);
  if (result.status !== 0) fail(`${path.basename(binary)} failed: ${(result.stderr || result.stdout).trim().slice(0, 400)}`);
  return result.stdout;
}

function renderPage(file, n, dpi, prefix, password) {
  const pdftoppm = which("pdftoppm");
  if (!pdftoppm) fail("pdftoppm (Poppler) is not installed here, so this skill cannot draw pages as pictures");
  const args = ["-png", "-r", String(dpi), "-f", String(n), "-l", String(n), "-singlefile"];
  if (password) args.push("-upw", password);
  run(pdftoppm, [...args, file, prefix]);
  return `${prefix}.png`;
}

async function render(file, outDir, options = {}) {
  const doc = await openForRead(file, options.password);
  const wanted = parseRanges(options.pages ?? "1", doc.numPages);
  await doc.loadingTask.destroy();
  const dpi = Number(options.dpi ?? 110);
  if (!(dpi >= 30 && dpi <= 600)) fail("--dpi must be between 30 and 600");
  fs.mkdirSync(outDir, { recursive: true });
  const base = path.basename(file, path.extname(file));
  const files = wanted.map((n) => renderPage(file, n, dpi, path.join(outDir, `${base}-page-${n}`), options.password));
  return { files, dpi };
}

async function ocr(file, options = {}) {
  const tesseract = which("tesseract");
  if (!tesseract) fail("tesseract is not installed here, so this skill cannot read scanned pages");
  const lang = options.lang ?? "eng";
  if (!/^[a-z_]+(\+[a-z_]+)*$/i.test(lang)) fail(`--lang "${lang}" is not a tesseract language list such as eng or eng+deu`);
  const format = options.format ?? "markdown";
  if (!["markdown", "text", "json"].includes(format)) fail("--format must be markdown, text or json");
  const dpi = Number(options.dpi ?? 300);
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "pdf-ocr-"));
  try {
    const { pages, total, maxChars, nextHint } = await collect(file, options, async (_page, n) => {
      const image = renderPage(file, n, dpi, path.join(tmp, `p${n}`), options.password);
      const text = run(tesseract, [image, "-", "-l", lang, "--psm", "3"]).replace(/\f/g, "").replace(/\n{3,}/g, "\n\n").trim();
      fs.rmSync(image, { force: true });
      return text ? { page: n, text } : { page: n, text: "", note: `tesseract found no text on page ${n}` };
    });
    return formatPages(pages, format, total, maxChars, nextHint);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

module.exports = { compactRanges, info, ocr, read, render, toCm };
