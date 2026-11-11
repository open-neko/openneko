#!/usr/bin/env node
"use strict";

process.env.TZ = "UTC";

const fs = require("node:fs");
const path = require("node:path");
const { Workbook } = require("./lib/workbook.cjs");
const { recalculate } = require("./lib/recalc.cjs");
const refs = require("./lib/refs.cjs");

const USAGE = `Usage:
  xlsx.cjs info <file.xlsx>
  xlsx.cjs read <file.xlsx> [--sheet NAME] [--range A1:F40] [--format markdown|csv|json] [--formulas] [--max-rows N] [--all-sheets]
  xlsx.cjs create <spec.json|-> <out.xlsx> [--no-recalc]
  xlsx.cjs edit <in.xlsx> <ops.json|-> <out.xlsx> [--no-recalc]
  xlsx.cjs recalc <in.xlsx> [out.xlsx]
  xlsx.cjs check <file.xlsx>`;

function fail(message) {
  process.stderr.write(`xlsx: ${message}\n`);
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
    if (["formulas", "no-recalc", "all-sheets"].includes(name)) flags[name] = true;
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

function openBook(file) {
  if (!fs.existsSync(file)) fail(`no such file: ${file}`);
  if (/\.(xls|xlsb|ods|csv)$/i.test(file)) fail(`${path.extname(file)} files are not supported; this skill reads and writes .xlsx and .xlsm`);
  return Workbook.open(new Uint8Array(fs.readFileSync(file)));
}

function writeBlock(sheet, start, rows) {
  if (!Array.isArray(rows)) throw new Error("rows must be a list of lists");
  const origin = refs.parseCell(start ?? "A1");
  rows.forEach((row, r) => {
    if (!Array.isArray(row)) throw new Error(`row ${r + 1} must be a list`);
    row.forEach((value, c) => {
      if (value === undefined) return;
      sheet.set(refs.cellName(origin.row + r, origin.col + c), value);
    });
  });
}

function forRange(range, fn) {
  const { from, to } = refs.parseRange(range);
  if ((to.row - from.row + 1) * (to.col - from.col + 1) > 1_000_000) throw new Error(`range ${range} is too large to style cell by cell`);
  for (let row = from.row; row <= to.row; row++) for (let col = from.col; col <= to.col; col++) fn(refs.cellName(row, col));
}

function applyOp(book, op, index) {
  const where = `op ${index + 1} (${op?.op ?? "?"})`;
  try {
    switch (op?.op) {
      case "set": {
        const { op: _op, sheet, cell, ...spec } = op;
        if (!cell) throw new Error("needs cell");
        book.sheet(sheet).set(cell, spec);
        break;
      }
      case "rows":
        writeBlock(book.sheet(op.sheet), op.start, op.rows);
        break;
      case "append": {
        const sheet = book.sheet(op.sheet);
        const extent = sheet.extent();
        writeBlock(sheet, refs.cellName(extent ? extent.to.row + 1 : 1, 1), op.rows);
        break;
      }
      case "style": {
        const sheet = book.sheet(op.sheet);
        forRange(op.range, (ref) => sheet.set(ref, { style: op.style }));
        break;
      }
      case "addSheet":
        book.addSheet(op.name);
        break;
      case "renameSheet":
        book.renameSheet(op.from, op.to);
        break;
      case "deleteSheet":
        book.deleteSheet(op.name);
        break;
      case "columns": {
        const sheet = book.sheet(op.sheet);
        for (const [letter, width] of Object.entries(op.widths ?? {})) {
          if (!(Number(width) > 0 && Number(width) <= 255)) throw new Error(`column ${letter}: width must be between 0 and 255`);
          sheet.setColumnWidth(refs.colToNumber(letter), Number(width));
        }
        break;
      }
      case "freeze":
        book.sheet(op.sheet).freeze(op.cell);
        break;
      case "merge":
        book.sheet(op.sheet).merge(op.range);
        break;
      case "name":
        book.defineName(op.name, op.ref, op.scope);
        break;
      default:
        throw new Error("unknown op; use set, rows, append, style, addSheet, renameSheet, deleteSheet, columns, freeze, merge or name");
    }
  } catch (error) {
    throw new Error(`${where}: ${error.message}`);
  }
}

function sheetOps(spec) {
  const ops = [{ op: "addSheet", name: spec.name }];
  if (spec.rows) ops.push({ op: "rows", sheet: spec.name, start: spec.start, rows: spec.rows });
  if (spec.columns) ops.push({ op: "columns", sheet: spec.name, widths: spec.columns });
  if (spec.freeze) ops.push({ op: "freeze", sheet: spec.name, cell: spec.freeze });
  for (const range of spec.merge ?? []) ops.push({ op: "merge", sheet: spec.name, range });
  for (const entry of spec.styles ?? []) ops.push({ op: "style", sheet: spec.name, range: entry.range, style: entry.style });
  return ops;
}

function finish(book, out, flags) {
  const summary = { file: out, sheets: book.sheetNames() };
  if (!flags["no-recalc"]) Object.assign(summary, recalculate(book));
  const tmp = `${out}.tmp-${process.pid}`;
  fs.writeFileSync(tmp, book.bytes());
  fs.renameSync(tmp, out);
  return summary;
}

function displayValue(cell, withFormulas, forText) {
  if (withFormulas && cell.formula !== undefined) return `=${cell.formula}`;
  if (cell.value === null) return forText ? "" : null;
  return cell.value;
}

function readSheet(book, sheetName, flags) {
  const sheet = book.sheet(sheetName);
  const extent = flags.range ? refs.parseRange(flags.range) : sheet.extent();
  if (!extent) return { sheet: sheet.name, range: null, rows: [] };
  const used = sheet.extent();
  const to = {
    row: Math.min(extent.to.row, used ? used.to.row : extent.from.row),
    col: Math.min(extent.to.col, used ? used.to.col : extent.from.col),
  };
  const maxRows = flags["max-rows"] ? Number(flags["max-rows"]) : 200;
  const lastRow = Math.min(to.row, extent.from.row + maxRows - 1);
  const rows = [];
  for (let row = extent.from.row; row <= lastRow; row++) {
    const line = [];
    for (let col = extent.from.col; col <= to.col; col++) {
      line.push(sheet.cell(refs.cellName(row, col)));
    }
    rows.push(line);
  }
  return {
    sheet: sheet.name,
    range: refs.rangeName({ from: extent.from, to: { row: lastRow, col: to.col } }),
    truncated: lastRow < to.row ? { shown: lastRow - extent.from.row + 1, total: to.row - extent.from.row + 1 } : undefined,
    rows,
  };
}

function csvField(value) {
  const text = value === null || value === undefined ? "" : String(value);
  return /[",\n\r]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

function render(result, format, withFormulas) {
  const values = result.rows.map((line) => line.map((cell) => displayValue(cell, withFormulas, format !== "json")));
  if (format === "json") {
    const out = { sheet: result.sheet, range: result.range, rows: values };
    if (withFormulas) {
      out.formulas = {};
      for (const line of result.rows) for (const cell of line) if (cell.formula !== undefined) out.formulas[cell.ref] = { formula: `=${cell.formula}`, value: cell.value };
    }
    if (result.truncated) out.truncated = result.truncated;
    return JSON.stringify(out, null, 2);
  }
  if (format === "csv") return values.map((line) => line.map(csvField).join(",")).join("\n");
  const firstCol = result.range ? refs.parseRange(result.range).from.col : 1;
  const firstRow = result.range ? refs.parseRange(result.range).from.row : 1;
  const width = values[0]?.length ?? 0;
  const header = ["", ...Array.from({ length: width }, (_, i) => refs.numberToCol(firstCol + i))];
  const cell = (value) => String(value ?? "").replace(/\|/g, "\\|").replace(/\n/g, " ");
  const lines = [`### ${result.sheet} (${result.range ?? "empty"})`, "", `| ${header.join(" | ")} |`, `|${header.map(() => "---").join("|")}|`];
  values.forEach((line, i) => lines.push(`| ${firstRow + i} | ${line.map(cell).join(" | ")} |`));
  if (result.truncated) lines.push("", `Showing ${result.truncated.shown} of ${result.truncated.total} rows. Use --range or --max-rows for more.`);
  return lines.join("\n");
}

function info(book) {
  return {
    sheets: book.sheetInfos().map((entry) => {
      if (!entry.worksheet) return { name: entry.name, kind: "not a worksheet" };
      const sheet = book.sheet(entry.name);
      const extent = sheet.extent();
      let formulas = 0;
      for (const ref of sheet.cells.keys()) if (sheet.cell(ref).formula !== undefined) formulas++;
      return { name: entry.name, state: entry.state, range: extent ? refs.rangeName(extent) : null, rows: extent ? extent.to.row - extent.from.row + 1 : 0, columns: extent ? extent.to.col - extent.from.col + 1 : 0, formulas };
    }),
    names: book.definedNames(),
    date1904: book.date1904,
  };
}

function main() {
  const [command, ...rest] = process.argv.slice(2);
  const { positional, flags } = parseArgs(rest);
  if (!command || command === "help" || command === "--help") {
    process.stdout.write(`${USAGE}\n`);
    return;
  }
  switch (command) {
    case "info": {
      if (positional.length !== 1) fail(USAGE);
      process.stdout.write(`${JSON.stringify(info(openBook(positional[0])), null, 2)}\n`);
      return;
    }
    case "read": {
      if (positional.length !== 1) fail(USAGE);
      const book = openBook(positional[0]);
      const format = flags.format ?? "markdown";
      if (!["markdown", "csv", "json"].includes(format)) fail("--format must be markdown, csv or json");
      const names = flags["all-sheets"] ? book.sheetNames() : [flags.sheet];
      const parts = names.map((name) => render(readSheet(book, name, flags), format, Boolean(flags.formulas)));
      process.stdout.write(`${parts.join(format === "json" ? "\n" : "\n\n")}\n`);
      return;
    }
    case "create": {
      if (positional.length !== 2) fail(USAGE);
      const spec = readJson(positional[0]);
      if (!Array.isArray(spec.sheets) || !spec.sheets.length) fail("the spec needs a sheets list with at least one sheet");
      const book = Workbook.blank();
      const ops = [...spec.sheets.flatMap(sheetOps), ...(spec.names ?? []).map((entry) => ({ op: "name", ...entry })), ...(spec.ops ?? [])];
      ops.forEach((op, i) => applyOp(book, op, i));
      process.stdout.write(`${JSON.stringify(finish(book, positional[1], flags), null, 2)}\n`);
      return;
    }
    case "edit": {
      if (positional.length !== 3) fail(USAGE);
      const book = openBook(positional[0]);
      const input = readJson(positional[1]);
      const ops = Array.isArray(input) ? input : input.ops;
      if (!Array.isArray(ops)) fail("ops must be a list, or an object with an ops list");
      ops.forEach((op, i) => applyOp(book, op, i));
      process.stdout.write(`${JSON.stringify(finish(book, positional[2], flags), null, 2)}\n`);
      return;
    }
    case "recalc": {
      if (positional.length < 1 || positional.length > 2) fail(USAGE);
      const book = openBook(positional[0]);
      process.stdout.write(`${JSON.stringify(finish(book, positional[1] ?? positional[0], {}), null, 2)}\n`);
      return;
    }
    case "check": {
      if (positional.length !== 1) fail(USAGE);
      const result = recalculate(openBook(positional[0]), { write: false });
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
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
