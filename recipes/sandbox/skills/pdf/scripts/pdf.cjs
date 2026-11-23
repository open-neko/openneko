#!/usr/bin/env node
"use strict";

const { UserError, loadForWrite, readJson, save } = require("./lib/common.cjs");
const { info, ocr, read, render } = require("./lib/read.cjs");
const { create } = require("./lib/build.cjs");
const { listFields } = require("./lib/forms.cjs");
const { edit, fillFile, merge, split } = require("./lib/edit.cjs");

const USAGE = `Usage:
  pdf.cjs info <file.pdf> [--password P]
  pdf.cjs read <file.pdf> [--pages 1-3,7] [--format markdown|text|json] [--layout] [--password P] [--max-chars N]
  pdf.cjs ocr <file.pdf> [--pages 1-3] [--lang eng] [--format markdown|text|json] [--password P]
  pdf.cjs render <file.pdf> <out-dir> [--pages 1] [--dpi 110] [--password P]
  pdf.cjs create <spec.json|-> <out.pdf>
  pdf.cjs edit <in.pdf> <ops.json|-> <out.pdf> [--break-signatures]
  pdf.cjs fields <file.pdf>
  pdf.cjs fill <in.pdf> <values.json|-> <out.pdf> [--flatten] [--font F.ttf] [--break-signatures]
  pdf.cjs merge <out.pdf> <in.pdf[:pages]>... [--bookmarks]
  pdf.cjs split <in.pdf> <out-dir> [--every N | --ranges "1-3 4-9"]`;

const FLAGS = new Set(["layout", "flatten", "bookmarks", "break-signatures"]);

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
    else throw new UserError(`--${name} needs a value`);
  }
  flags.breakSignatures = Boolean(flags["break-signatures"]);
  return { positional, flags };
}

function need(positional, count) {
  if (positional.length < count) throw new UserError(`missing arguments\n${USAGE}`);
}

function print(value) {
  process.stdout.write(typeof value === "string" ? `${value}\n` : `${JSON.stringify(value, null, 2)}\n`);
}

async function main() {
  const [command, ...rest] = process.argv.slice(2);
  const { positional, flags } = parseArgs(rest);
  switch (command) {
    case "info":
      need(positional, 1);
      return print(await info(positional[0], flags));
    case "read":
      need(positional, 1);
      return print(await read(positional[0], flags));
    case "ocr":
      need(positional, 1);
      return print(await ocr(positional[0], flags));
    case "render":
      need(positional, 2);
      return print(await render(positional[0], positional[1], flags));
    case "create": {
      need(positional, 2);
      const doc = await create(readJson(positional[0]));
      return print(await save(doc, positional[1]));
    }
    case "edit": {
      need(positional, 3);
      const { doc, notes } = await edit(positional[0], readJson(positional[1]), flags);
      return print(await save(doc, positional[2], notes.length ? { notes } : {}));
    }
    case "fields": {
      need(positional, 1);
      return print(listFields(await loadForWrite(positional[0], { breakSignatures: true })));
    }
    case "fill": {
      need(positional, 3);
      const { doc, notes } = await fillFile(positional[0], readJson(positional[1]), flags);
      return print(await save(doc, positional[2], { notes, flattened: Boolean(flags.flatten) }));
    }
    case "merge": {
      need(positional, 2);
      const { doc, renamed } = await merge(positional.slice(1), flags);
      return print(await save(doc, positional[0], renamed.length ? { renamedFields: renamed } : {}));
    }
    case "split":
      need(positional, 2);
      return print(await split(positional[0], positional[1], flags));
    case undefined:
    case "help":
    case "--help":
      return print(USAGE);
    default:
      throw new UserError(`unknown command "${command}"\n${USAGE}`);
  }
}

main().catch((error) => {
  process.stderr.write(error instanceof UserError ? `pdf: ${error.message}\n` : `pdf: ${error?.stack ?? error}\n`);
  process.exit(1);
});
