#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const x = require("./lib/ooxml.cjs");
const T = require("./lib/template.cjs");
const B = require("./lib/build.cjs");
const { Document } = require("./lib/document.cjs");
const { applyOp } = require("./lib/edit.cjs");
const { info, listChanges, listComments, readDocument } = require("./lib/read.cjs");

const USAGE = `Usage:
  docx.cjs info <file.docx>
  docx.cjs read <file.docx> [--format markdown|text|json] [--changes show|accept|reject] [--numbers] [--comments] [--from N] [--to N] [--max-chars N]
  docx.cjs changes <file.docx>
  docx.cjs comments <file.docx>
  docx.cjs create <spec.json|-> <out.docx>
  docx.cjs edit <in.docx> <ops.json|-> <out.docx> [--track] [--author NAME]`;

const FLAGS = new Set(["numbers", "comments", "track"]);

function fail(message) {
  process.stderr.write(`docx: ${message}\n`);
  process.exit(1);
}

function parseArgs(argv) {
  const positional = [];
  const flags = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg.startsWith("--")) {
      positional.push(arg);
      continue;
    }
    const name = arg.slice(2);
    if (FLAGS.has(name)) flags[name] = true;
    else if (i + 1 < argv.length) flags[name] = argv[++i];
    else fail(`--${name} needs a value`);
  }
  return { positional, flags };
}

function readJson(source) {
  const text = source === "-" ? fs.readFileSync(0, "utf8") : fs.readFileSync(source, "utf8");
  try {
    return JSON.parse(text);
  } catch (error) {
    fail(`${source === "-" ? "stdin" : source} is not valid JSON: ${error.message}`);
  }
}

function openDoc(file) {
  if (!fs.existsSync(file)) fail(`no such file: ${file}`);
  if (/\.(doc|rtf|odt|pages)$/i.test(file)) fail(`${path.extname(file)} files are not supported; this skill reads and writes .docx and .docm`);
  return Document.open(new Uint8Array(fs.readFileSync(file)));
}

function now() {
  return new Date().toISOString().replace(/\.\d+Z$/, "Z");
}

function context(doc, options) {
  const date = now();
  const author = options.author ?? "OpenNeko";
  const trackFor = (who) => ({ author: who ?? author, date, id: () => doc.nextId() });
  return { doc, part: doc.mainPath, author, date, trackFor, track: options.track ? trackFor() : null };
}

function save(doc, out, extra) {
  const tmp = `${out}.tmp-${process.pid}`;
  fs.writeFileSync(tmp, doc.bytes());
  fs.renameSync(tmp, out);
  return { file: out, paragraphs: doc.paragraphs().length, ...extra };
}

function setPage(doc, page) {
  const sect = doc.sectPr();
  const size = x.child(sect, "w:pgSz");
  const margin = x.child(sect, "w:pgMar");
  if (page.size !== undefined) {
    const dims = T.PAGE_SIZES[String(page.size).toLowerCase()];
    if (!dims) throw new Error(`page size must be one of ${Object.keys(T.PAGE_SIZES).join(", ")}`);
    Object.assign(x.attrs(size), { "w:w": String(dims[0]), "w:h": String(dims[1]) });
  }
  if (page.orientation !== undefined) {
    if (!["portrait", "landscape"].includes(page.orientation)) throw new Error("orientation must be portrait or landscape");
    const a = x.attrs(size);
    const [w, h] = [Number(a["w:w"]), Number(a["w:h"])];
    const landscape = page.orientation === "landscape";
    a["w:w"] = String(landscape ? Math.max(w, h) : Math.min(w, h));
    a["w:h"] = String(landscape ? Math.min(w, h) : Math.max(w, h));
    if (landscape) a["w:orient"] = "landscape";
    else delete a["w:orient"];
  }
  if (page.margins !== undefined) {
    const m = typeof page.margins === "number" ? { top: page.margins, bottom: page.margins, left: page.margins, right: page.margins } : page.margins;
    for (const side of ["top", "bottom", "left", "right"]) {
      if (m[side] === undefined) continue;
      const cm = Number(m[side]);
      if (!(cm >= 0 && cm < 20)) throw new Error(`margin ${side} must be 0 to 20 cm`);
      x.attrs(margin)[`w:${side}`] = String(Math.round(cm * 567));
    }
  }
}

function addHeaderFooter(doc, kind, spec, ctx) {
  const value = typeof spec === "string" ? { text: spec } : spec;
  if (!value || value.text === undefined) throw new Error(`${kind} must be text or { "text": ..., "align": ... }`);
  let n = 1;
  while (doc.pkg.files.has(`word/${kind}${n}.xml`)) n++;
  const part = `word/${kind}${n}.xml`;
  const rootTag = kind === "header" ? "w:hdr" : "w:ftr";
  doc.pkg.setXml(part, x.parseXml(`${T.HEAD}<${rootTag} ${T.ROOT_NS}></${rootTag}>`));
  doc.pkg.setOverride(part, T.CONTENT_TYPES[kind]);
  const rId = doc.pkg.addRel(doc.mainPath, T.REL_TYPES[kind], `${kind}${n}.xml`);
  const local = { ...ctx, part };
  const p = x.el("w:p", null, [B.pPrFor({ align: value.align ?? (kind === "footer" ? "center" : "left") }, local), ...B.fieldText(value.text, local)]);
  x.kids(x.rootOf(doc.pkg.xml(part))).push(p);
  const sect = doc.sectPr();
  const list = x.kids(sect);
  const refs = list.filter((node) => ["w:headerReference", "w:footerReference"].includes(x.tagOf(node))).length;
  list.splice(kind === "header" ? 0 : refs, 0, x.el(`w:${kind}Reference`, { "w:type": "default", "r:id": rId }));
}

function create(spec) {
  if (!spec || typeof spec !== "object" || Array.isArray(spec)) throw new Error("the spec must be an object with a blocks list");
  if (!Array.isArray(spec.blocks)) throw new Error("the spec needs a blocks list");
  const props = spec.properties ?? {};
  const doc = Document.blank({ title: props.title, author: props.author, font: spec.font });
  if (props.subject || props.keywords || props.description) doc.setProperties({ subject: props.subject, keywords: props.keywords, description: props.description });
  const ctx = context(doc, { author: props.author });
  if (spec.page) setPage(doc, spec.page);
  if (spec.header !== undefined) addHeaderFooter(doc, "header", spec.header, ctx);
  if (spec.footer !== undefined) addHeaderFooter(doc, "footer", spec.footer, ctx);
  const nodes = B.blockNodes(spec.blocks, ctx);
  const list = x.kids(doc.body);
  list.splice(list.length - 1, 0, ...nodes);
  if (ctx.usedFields) doc.setSetting("w:updateFields", true);
  if (Array.isArray(spec.ops)) spec.ops.forEach((op, i) => applyOp(doc, op, i, ctx));
  return doc;
}

function main() {
  const [command, ...rest] = process.argv.slice(2);
  const { positional, flags } = parseArgs(rest);
  if (!command || command === "help" || command === "--help") {
    process.stdout.write(`${USAGE}\n`);
    return;
  }
  const print = (value) => process.stdout.write(`${typeof value === "string" ? value : JSON.stringify(value, null, 2)}\n`);
  switch (command) {
    case "info":
      if (positional.length !== 1) fail(USAGE);
      print(info(openDoc(positional[0])));
      return;
    case "read": {
      if (positional.length !== 1) fail(USAGE);
      const format = flags.format ?? "markdown";
      if (!["markdown", "text", "json"].includes(format)) fail("--format must be markdown, text or json");
      const changes = flags.changes ?? "show";
      if (!["show", "accept", "reject"].includes(changes)) fail("--changes must be show, accept or reject");
      const number = (name) => {
        if (flags[name] === undefined) return undefined;
        const value = Number(flags[name]);
        if (!(value >= 1)) fail(`--${name} must be a number of 1 or more`);
        return value;
      };
      print(readDocument(openDoc(positional[0]), { format, changes, numbers: Boolean(flags.numbers), comments: Boolean(flags.comments), from: number("from"), to: number("to"), maxChars: number("max-chars") }));
      return;
    }
    case "changes":
      if (positional.length !== 1) fail(USAGE);
      print(listChanges(openDoc(positional[0])));
      return;
    case "comments":
      if (positional.length !== 1) fail(USAGE);
      print(listComments(openDoc(positional[0])));
      return;
    case "create": {
      if (positional.length !== 2) fail(USAGE);
      const doc = create(readJson(positional[0]));
      print(save(doc, positional[1], {}));
      return;
    }
    case "edit": {
      if (positional.length !== 3) fail(USAGE);
      const doc = openDoc(positional[0]);
      const input = readJson(positional[1]);
      const ops = Array.isArray(input) ? input : input?.ops;
      if (!Array.isArray(ops)) fail("ops must be a list, or an object with an ops list");
      const track = flags.track || (!Array.isArray(input) && input.track === true);
      const author = flags.author ?? (!Array.isArray(input) ? input.author : undefined);
      const ctx = context(doc, { track, author });
      const results = ops.map((op, i) => applyOp(doc, op, i, ctx));
      doc.touchModified(ctx.author, ctx.date);
      print(save(doc, positional[2], { tracked: Boolean(track), results }));
      return;
    }
    default:
      fail(`unknown command "${command}"\n${USAGE}`);
  }
}

try {
  main();
} catch (error) {
  fail(error.message);
}
