"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const { PDFDocument, PDFName, PDFDict, PDFArray, PDFRef } = require("pdf-lib");

const CM = 72 / 2.54;

const PAGE_SIZES = {
  A3: [841.89, 1190.55],
  A4: [595.28, 841.89],
  A5: [419.53, 595.28],
  Letter: [612, 792],
  Legal: [612, 1008],
  Tabloid: [792, 1224],
};

class UserError extends Error {}

function fail(message) {
  throw new UserError(message);
}

function cm(value) {
  return value * CM;
}

function toCm(points) {
  return Math.round((points / CM) * 100) / 100;
}

function pageSize(page = {}) {
  let size = page.size ?? "A4";
  let dims;
  if (Array.isArray(size)) dims = [cm(size[0]), cm(size[1])];
  else {
    const key = Object.keys(PAGE_SIZES).find((name) => name.toLowerCase() === String(size).toLowerCase());
    if (!key) fail(`unknown page size "${size}"; use ${Object.keys(PAGE_SIZES).join(", ")} or [width, height] in cm`);
    dims = [...PAGE_SIZES[key]];
  }
  const landscape = page.orientation === "landscape";
  if (page.orientation && !["portrait", "landscape"].includes(page.orientation)) fail(`orientation must be portrait or landscape`);
  if (landscape !== dims[0] > dims[1]) dims.reverse();
  return dims;
}

function sizeName(width, height) {
  const [w, h] = width < height ? [width, height] : [height, width];
  for (const [name, [sw, sh]] of Object.entries(PAGE_SIZES)) {
    if (Math.abs(sw - w) < 2 && Math.abs(sh - h) < 2) return width > height ? `${name} landscape` : name;
  }
  return `${toCm(width)} x ${toCm(height)} cm`;
}

function parseRanges(spec, count, what = "pages") {
  if (spec === undefined || spec === null || spec === "" || spec === "all") return Array.from({ length: count }, (_, i) => i + 1);
  if (typeof spec === "number") spec = String(spec);
  if (Array.isArray(spec)) spec = spec.join(",");
  if (typeof spec !== "string") fail(`${what} must be a page number, a range such as "2-4,7" or "all"`);
  const out = [];
  for (const raw of spec.split(",")) {
    const part = raw.trim().toLowerCase();
    if (!part) continue;
    const match = /^(\d+|last)?\s*(-)?\s*(\d+|last)?$/.exec(part);
    if (!match || (!match[1] && !match[3])) fail(`"${raw.trim()}" is not a page range; use forms such as 3, 2-5, 4- or last`);
    const num = (token, fallback) => (token === undefined ? fallback : token === "last" ? count : Number(token));
    const start = num(match[1], 1);
    const end = match[2] ? num(match[3], count) : start;
    if (start < 1 || end > count || start > end) fail(`${what} "${raw.trim()}" is outside 1-${count}`);
    for (let p = start; p <= end; p++) out.push(p);
  }
  if (!out.length) fail(`${what} "${spec}" names no page`);
  return out;
}

function readJson(source) {
  const text = source === "-" ? fs.readFileSync(0, "utf8") : readFile(source).toString("utf8");
  try {
    return JSON.parse(text);
  } catch (error) {
    fail(`${source === "-" ? "stdin" : source} is not valid JSON: ${error.message}`);
  }
}

function readFile(file) {
  if (!fs.existsSync(file)) fail(`no such file: ${file}`);
  return fs.readFileSync(file);
}

function checkPdf(file, bytes) {
  const head = bytes.subarray(0, 1024).toString("latin1");
  if (!head.includes("%PDF-")) {
    if (/\.(docx?|xlsx?|pptx?)$/i.test(file)) fail(`${path.basename(file)} is not a PDF; use the ${path.extname(file).slice(1, 5).replace(/x$/, "")}x skill`);
    fail(`${path.basename(file)} is not a PDF file`);
  }
}

function signatureFields(doc) {
  const acro = doc.catalog.lookupMaybe(PDFName.of("AcroForm"), PDFDict);
  if (!acro) return [];
  const names = [];
  const walk = (ref, prefix, depth) => {
    if (depth > 30) return;
    const dict = doc.context.lookup(ref);
    if (!(dict instanceof PDFDict)) return;
    const t = dict.get(PDFName.of("T"));
    const name = t ? [prefix, t.decodeText ? t.decodeText() : String(t)].filter(Boolean).join(".") : prefix;
    if (dict.get(PDFName.of("FT")) === PDFName.of("Sig") && dict.get(PDFName.of("V"))) names.push(name || "(unnamed)");
    const kids = dict.lookupMaybe(PDFName.of("Kids"), PDFArray);
    if (kids) for (let i = 0; i < kids.size(); i++) walk(kids.get(i), name, depth + 1);
  };
  const fields = acro.lookupMaybe(PDFName.of("Fields"), PDFArray);
  if (fields) for (let i = 0; i < fields.size(); i++) walk(fields.get(i), "", 0);
  return names;
}

async function loadForWrite(file, options = {}) {
  const bytes = readFile(file);
  checkPdf(file, bytes);
  let doc;
  try {
    doc = await PDFDocument.load(bytes, { ignoreEncryption: true, updateMetadata: false });
  } catch (error) {
    fail(`cannot open ${path.basename(file)}: ${error.message}`);
  }
  if (doc.isEncrypted) fail(`${path.basename(file)} is encrypted. This skill reads encrypted files with --password, but cannot change them. Ask the user for a copy without a password.`);
  const signed = signatureFields(doc);
  if (signed.length && !options.breakSignatures) {
    fail(`${path.basename(file)} is digitally signed (${signed.join(", ")}). Any change makes the signature invalid. Tell the user, and add --break-signatures only when they agree.`);
  }
  return doc;
}

async function save(doc, out, extra = {}) {
  doc.setModificationDate(new Date());
  const bytes = await doc.save();
  fs.mkdirSync(path.dirname(path.resolve(out)), { recursive: true });
  const tmp = `${out}.tmp-${process.pid}`;
  fs.writeFileSync(tmp, bytes);
  fs.renameSync(tmp, out);
  return { file: out, pages: doc.getPageCount(), bytes: bytes.length, ...extra };
}

let pdfjsPromise;
function pdfjs() {
  if (!pdfjsPromise) {
    const quiet = { log: console.log, warn: console.warn };
    console.log = () => {};
    console.warn = () => {};
    pdfjsPromise = import(pathToFileURL(require.resolve("pdfjs-dist/legacy/build/pdf.mjs")).href).finally(() => {
      console.log = quiet.log;
      console.warn = quiet.warn;
    });
  }
  return pdfjsPromise;
}

async function openForRead(file, password) {
  const bytes = readFile(file);
  checkPdf(file, bytes);
  const lib = await pdfjs();
  const task = lib.getDocument({
    data: new Uint8Array(bytes),
    password,
    cMapUrl: `${path.join(path.dirname(require.resolve("pdfjs-dist/legacy/build/pdf.mjs")), "..", "..", "cmaps")}${path.sep}`,
    cMapPacked: true,
    verbosity: 0,
    isEvalSupported: false,
    useSystemFonts: false,
    stopAtErrors: false,
  });
  try {
    return await task.promise;
  } catch (error) {
    if (error?.name === "PasswordException") {
      fail(password ? `the password for ${path.basename(file)} is wrong` : `${path.basename(file)} needs a password; add --password`);
    }
    fail(`cannot read ${path.basename(file)}: ${error?.message ?? error}`);
  }
}

function pageRefIndex(doc) {
  const index = new Map();
  doc.getPages().forEach((page, i) => index.set(page.ref.toString(), i));
  return index;
}

function asRef(value) {
  return value instanceof PDFRef ? value : undefined;
}

module.exports = {
  CM,
  PAGE_SIZES,
  UserError,
  asRef,
  checkPdf,
  cm,
  fail,
  loadForWrite,
  openForRead,
  pageRefIndex,
  pageSize,
  parseRanges,
  pdfjs,
  readFile,
  readJson,
  save,
  signatureFields,
  sizeName,
  toCm,
};
