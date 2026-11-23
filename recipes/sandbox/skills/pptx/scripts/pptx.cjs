#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const path = require("node:path");
const T = require("./lib/template.cjs");
const B = require("./lib/build.cjs");
const S = require("./lib/shapes.cjs");
const { Deck } = require("./lib/deck.cjs");
const { applyOp } = require("./lib/edit.cjs");
const { info, layoutsInfo, readDeck } = require("./lib/read.cjs");

const USAGE = `Usage:
  pptx.cjs info <file.pptx>
  pptx.cjs layouts <file.pptx|template.potx>
  pptx.cjs read <file.pptx> [--format markdown|text|json] [--slides 2-4,7] [--ids] [--no-notes] [--max-chars N]
  pptx.cjs create <spec.json|-> <out.pptx>
  pptx.cjs edit <in.pptx> <ops.json|-> <out.pptx>`;

const FLAGS = new Set(["ids", "no-notes"]);
const READS = new Set([".pptx", ".pptm", ".potx", ".potm", ".ppsx", ".ppsm"]);
const WRITES = new Set([".pptx", ".pptm", ".potx", ".potm"]);

function fail(message) {
  process.stderr.write(`pptx: ${message}\n`);
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

function openDeck(file) {
  if (!fs.existsSync(file)) fail(`no such file: ${file}`);
  const ext = path.extname(file).toLowerCase();
  if (!READS.has(ext)) fail(`${ext || "this"} files are not supported; this skill reads .pptx, .pptm, .potx, .potm, .ppsx and .ppsm`);
  return Deck.open(new Uint8Array(fs.readFileSync(file)));
}

function checkOut(out) {
  const ext = path.extname(out).toLowerCase();
  if (!WRITES.has(ext)) fail(`the output must end in .pptx, .pptm, .potx or .potm; got ${JSON.stringify(out)}`);
  return ext;
}

function save(deck, out, extra) {
  const notes = deck.prepareFor(path.extname(out));
  const tmp = `${out}.tmp-${process.pid}`;
  fs.writeFileSync(tmp, deck.bytes());
  fs.renameSync(tmp, out);
  return { file: out, slides: deck.slides().length, ...extra, notes: notes.length ? notes : undefined };
}

function slideSize(value) {
  if (value === undefined) return T.SLIDE_SIZES["16:9"];
  if (typeof value === "string") {
    const size = T.SLIDE_SIZES[value];
    if (!size) throw new Error(`size must be ${Object.keys(T.SLIDE_SIZES).join(", ")} or { "w": cm, "h": cm }`);
    return size;
  }
  const w = S.cm(value?.w, "size w");
  const h = S.cm(value?.h, "size h");
  if (w < 914400 || h < 914400 || w > 51206400 || h > 51206400) throw new Error("size w and h must be 2.54 to 142.24 cm");
  return [w, h];
}

function now() {
  return new Date().toISOString().replace(/\.\d+Z$/, "Z");
}

const CREATE_KEYS = new Set(["template", "keepSlides", "size", "properties", "footer", "slideNumbers", "slides", "ops"]);

function create(spec) {
  if (!spec || typeof spec !== "object" || Array.isArray(spec)) throw new Error("the spec must be an object with a slides list");
  for (const key of Object.keys(spec)) if (!CREATE_KEYS.has(key)) throw new Error(`unknown spec field "${key}"; use ${[...CREATE_KEYS].join(", ")}`);
  if (!Array.isArray(spec.slides)) throw new Error("the spec needs a slides list");
  let deck;
  if (spec.template !== undefined) {
    if (spec.size !== undefined) throw new Error("a template sets the slide size; remove size or template");
    deck = openDeck(String(spec.template));
    if (!spec.keepSlides) {
      for (const slide of deck.slides().reverse()) deck.removeSlide(slide);
      deck.dropSections();
    }
  } else deck = Deck.blank(slideSize(spec.size));
  const props = spec.properties ?? {};
  if (Object.keys(props).length) deck.setProperties(props);
  deck.touchModified(props.author, now());
  const defaults = { footer: spec.footer, slideNumbers: spec.slideNumbers };
  spec.slides.forEach((slide, i) => {
    try {
      B.buildSlide(deck, slide, undefined, defaults);
    } catch (error) {
      throw new Error(`slide ${i + 1}: ${error.message}`);
    }
  });
  const results = Array.isArray(spec.ops) ? spec.ops.map((op, i) => applyOp(deck, op, i)) : undefined;
  return { deck, results };
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
      print(info(openDeck(positional[0])));
      return;
    case "layouts":
      if (positional.length !== 1) fail(USAGE);
      print(layoutsInfo(openDeck(positional[0])));
      return;
    case "read": {
      if (positional.length !== 1) fail(USAGE);
      const format = flags.format ?? "markdown";
      if (!["markdown", "text", "json"].includes(format)) fail("--format must be markdown, text or json");
      let maxChars;
      if (flags["max-chars"] !== undefined) {
        maxChars = Number(flags["max-chars"]);
        if (!(maxChars >= 1)) fail("--max-chars must be a number of 1 or more");
      }
      print(readDeck(openDeck(positional[0]), { format, slides: flags.slides, ids: Boolean(flags.ids), notes: !flags["no-notes"], maxChars }));
      return;
    }
    case "create": {
      if (positional.length !== 2) fail(USAGE);
      checkOut(positional[1]);
      const { deck, results } = create(readJson(positional[0]));
      print(save(deck, positional[1], { results }));
      return;
    }
    case "edit": {
      if (positional.length !== 3) fail(USAGE);
      checkOut(positional[2]);
      const deck = openDeck(positional[0]);
      const input = readJson(positional[1]);
      const ops = Array.isArray(input) ? input : input?.ops;
      if (!Array.isArray(ops)) fail("ops must be a list, or an object with an ops list");
      const results = ops.map((op, i) => applyOp(deck, op, i));
      deck.touchModified(Array.isArray(input) ? undefined : input.author, now());
      print(save(deck, positional[2], { results }));
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
