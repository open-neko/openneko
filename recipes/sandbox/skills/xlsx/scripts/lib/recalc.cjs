"use strict";

const FormulaParser = require("fast-formula-parser");
const formulajs = require("@formulajs/formulajs");
const { attrs, child, el, insertOrdered, removeChildren, textNode, textOf } = require("./ooxml.cjs");
const refs = require("./refs.cjs");

const { FormulaError } = FormulaParser;
const CELL_ORDER = ["f", "v", "is", "extLst"];
const ERROR_CODES = new Set(["#NULL!", "#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A"]);

function toError(code) {
  return new FormulaError(ERROR_CODES.has(code) ? code : "#VALUE!");
}

function normalize(result) {
  if (result instanceof Error && !(result instanceof FormulaError)) throw toError(result.message);
  if (result instanceof Date) return (result.getTime() - Date.UTC(1899, 11, 30)) / 86400000;
  return result;
}

function flat(value) {
  return Array.isArray(value) ? value.flat(Infinity) : [value];
}

function xlookup(lookup, lookupArray, returnArray, ifNotFound, matchMode, searchMode) {
  const keys = flat(lookupArray.value);
  const rows = Array.isArray(returnArray.value) ? returnArray.value : [[returnArray.value]];
  const vertical = rows.length > 1 || rows[0].length === 1;
  const mode = matchMode && !matchMode.omitted ? Number(matchMode.value) : 0;
  if (mode !== 0) throw FormulaError.NOT_IMPLEMENTED("XLOOKUP match_mode other than 0");
  const order = searchMode && !searchMode.omitted && Number(searchMode.value) === -1 ? [...keys.keys()].reverse() : [...keys.keys()];
  const wanted = typeof lookup.value === "string" ? lookup.value.toLowerCase() : lookup.value;
  for (const i of order) {
    const key = typeof keys[i] === "string" ? keys[i].toLowerCase() : keys[i];
    if (key !== wanted) continue;
    return vertical ? rows[i]?.[0] : rows[0]?.[i];
  }
  if (ifNotFound && !ifNotFound.omitted) return ifNotFound.value;
  throw FormulaError.NA;
}

const PREFER_FORMULAJS = new Set(["EDATE"]);
const CONTEXT_FIRST = new Set(["CHOOSE", "INDEX", "OFFSET", "INDIRECT", "IF", "WEBSERVICE", "ROW", "ROWS", "COLUMN", "COLUMNS", "SUMIF", "AVERAGEIF"]);
const probe = { value: 0, isArray: false };
const working = new Set();
for (const [name, fn] of Object.entries(new FormulaParser({}).functions)) {
  let result;
  try {
    result = fn(probe, probe, probe, probe);
  } catch {
    result = null;
  }
  if (result !== undefined && !PREFER_FORMULAJS.has(name)) working.add(name);
}
const extraFunctions = { XLOOKUP: xlookup };
(function bridge(source, prefix) {
  for (const [name, fn] of Object.entries(source)) {
    const full = prefix ? `${prefix}.${name}` : name;
    if (fn && typeof fn === "object") bridge(fn, full);
    if (typeof fn !== "function" || working.has(full) || extraFunctions[full]) continue;
    const call = (...args) => normalize(fn(...args.map((arg) => (arg.omitted ? undefined : arg.value))));
    extraFunctions[full] = CONTEXT_FIRST.has(full) ? (_parser, ...args) => call(...args) : call;
  }
})(formulajs, "");

function sortedInsert(list, value) {
  let lo = 0;
  let hi = list.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (list[mid] < value) lo = mid + 1;
    else hi = mid;
  }
  list.splice(lo, 0, value);
}

function firstAtLeast(list, value) {
  let lo = 0;
  let hi = list.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (list[mid] < value) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

function recalculate(book, { write = true } = {}) {
  const sheets = book.sheets();
  const bySheet = new Map();
  const formulaCols = new Map();
  for (const sheet of sheets) {
    sheet.expandSharedFormulas();
    bySheet.set(sheet.name.toLowerCase(), sheet);
    const cols = new Map();
    for (const [ref, cell] of sheet.cells) {
      const f = child(cell, "f");
      if (!f || attrs(f).t === "array") continue;
      const { row, col } = refs.parseCell(ref);
      if (!cols.has(col)) cols.set(col, []);
      sortedInsert(cols.get(col), row);
    }
    formulaCols.set(sheet.name.toLowerCase(), cols);
  }
  const names = book.definedNames();
  const memo = new Map();
  const open = new Set();
  const circular = new Set();
  const unsupported = new Set();
  const failures = new Map();
  const uncached = new Set();

  const keyOf = (sheet, row, col) => `${sheet.name.toLowerCase()}!${refs.cellName(row, col)}`;

  function sheetFor(name, fallback) {
    if (!name) return fallback;
    const sheet = bySheet.get(String(name).toLowerCase());
    if (!sheet) throw FormulaError.REF;
    return sheet;
  }

  function nameRef(name, sheetName) {
    const lower = name.toLowerCase();
    const local = names.find((entry) => entry.name.toLowerCase() === lower && entry.sheet && entry.sheet.toLowerCase() === String(sheetName).toLowerCase());
    const found = local ?? names.find((entry) => entry.name.toLowerCase() === lower && !entry.sheet);
    if (!found) return null;
    const match = /^(?:('(?:[^']|'')+'|[^!]+)!)?(\$?[A-Za-z]{0,3}\$?\d*(?::\$?[A-Za-z]{0,3}\$?\d*)?)$/.exec(found.ref.trim());
    if (!match || !match[2]) return null;
    let range;
    try {
      range = refs.parseRange(match[2].replace(/\$/g, ""));
    } catch {
      return null;
    }
    const sheet = match[1] ? refs.unquoteSheet(match[1]) : sheetName;
    if (range.from.row === range.to.row && range.from.col === range.to.col) return { sheet, row: range.from.row, col: range.from.col };
    return { sheet, from: range.from, to: range.to };
  }

  function formulaCellsIn(sheet, range) {
    const cols = formulaCols.get(sheet.name.toLowerCase());
    const out = [];
    if (!cols) return out;
    for (const [col, rows] of cols) {
      if (col < range.from.col || col > range.to.col) continue;
      for (let i = firstAtLeast(rows, range.from.row); i < rows.length && rows[i] <= range.to.row; i++) out.push(keyOf(sheet, rows[i], col));
    }
    return out;
  }

  function dependencies(sheet, formula) {
    const found = [];
    const addRange = (target, rangeText) => {
      try {
        found.push(...formulaCellsIn(target, refs.parseRange(rangeText.replace(/\$/g, ""))));
      } catch {
        // A reference that does not parse evaluates to an error later.
      }
    };
    formula.replace(/("(?:[^"]|"")*")|((?:'(?:[^']|'')+'|[A-Za-z_][A-Za-z0-9_.]*)!)?(\$?[A-Za-z]{1,3}\$?\d+(?::\$?[A-Za-z]{1,3}\$?\d+)?|\$?[A-Za-z]{1,3}:\$?[A-Za-z]{1,3}|\$?\d+:\$?\d+)(?![A-Za-z0-9_(.!])/g, (whole, str, prefix, ref) => {
      if (str !== undefined) return whole;
      const target = prefix ? bySheet.get(refs.unquoteSheet(prefix.slice(0, -1)).toLowerCase()) : sheet;
      if (target) addRange(target, ref);
      return whole;
    });
    for (const entry of names) {
      if (!new RegExp(`(?<![A-Za-z0-9_.!])${entry.name.replace(/[.\\]/g, "\\$&")}(?![A-Za-z0-9_(.!])`, "i").test(formula.replace(/"(?:[^"]|"")*"/g, ""))) continue;
      const ref = nameRef(entry.name, sheet.name);
      const target = ref && bySheet.get(String(ref.sheet).toLowerCase());
      if (!target) continue;
      found.push(...formulaCellsIn(target, ref.from ? ref : { from: ref, to: ref }));
    }
    return found;
  }

  function plainValue(sheet, ref) {
    const cell = sheet.cell(ref);
    switch (cell.type) {
      case "number":
      case "string":
      case "boolean":
        return cell.value;
      case "date":
        return cell.serial;
      case "error":
        return toError(cell.value);
      default:
        return null;
    }
  }

  const pool = [];
  let depth = 0;
  const config = {
    functions: extraFunctions,
    onCell: ({ sheet, row, col }) => valueOf(sheetFor(sheet, current.sheet), row, col),
    onRange: ({ sheet, from, to }) => {
      const target = sheetFor(sheet, current.sheet);
      const extent = target.extent();
      const out = [];
      const lastRow = Math.min(to.row, extent ? extent.to.row : 0);
      const lastCol = Math.min(to.col, extent ? extent.to.col : 0);
      for (let row = from.row; row <= Math.max(lastRow, from.row); row++) {
        const line = [];
        for (let col = from.col; col <= Math.max(lastCol, from.col); col++) line.push(valueOf(target, row, col));
        out.push(line);
      }
      return out;
    },
    onVariable: (name, sheetName) => nameRef(name, sheetName ?? current.sheet.name),
  };
  let current = { sheet: sheets[0] };

  function compute(sheet, row, col) {
    const key = keyOf(sheet, row, col);
    const cell = sheet.cells.get(refs.cellName(row, col));
    const formula = refs.fromFileFormula(textOf(child(cell, "f")));
    const parser = (pool[depth] ??= new FormulaParser(config));
    const saved = current;
    current = { sheet };
    depth++;
    let result;
    try {
      result = parser.parse(formula, { sheet: sheet.name, row, col });
    } catch (error) {
      if (error instanceof RangeError) {
        failures.set(key, "the formula chain is too deep to evaluate");
        uncached.add(key);
        result = FormulaError.VALUE;
      } else if (error instanceof FormulaError) {
        const missing = /^Function (.+) is not implemented\.?$/.exec(error.message ?? "");
        if (missing) {
          unsupported.add(missing[1]);
          failures.set(key, `function ${missing[1]} is not supported here`);
          uncached.add(key);
        } else if (error.error === "#ERROR!") {
          failures.set(key, `the formula does not parse: ${String(error.message).split("\n")[0]}`);
          uncached.add(key);
        }
        result = error.error === "#ERROR!" ? FormulaError.NAME : error;
      } else {
        failures.set(key, String(error.message ?? error));
        result = FormulaError.VALUE;
      }
    } finally {
      depth--;
      current = saved;
    }
    if (Array.isArray(result)) result = Array.isArray(result[0]) ? result[0][0] : result[0];
    if (result === undefined || result === null) result = 0;
    if (typeof result === "number") result = Number.isFinite(result) ? Number(result.toPrecision(15)) : FormulaError.NUM;
    memo.set(key, result);
    return result;
  }

  function ensure(startKey, start) {
    const stack = [start];
    while (stack.length) {
      const top = stack[stack.length - 1];
      const key = keyOf(top.sheet, top.row, top.col);
      if (memo.has(key)) {
        stack.pop();
        continue;
      }
      if (!open.has(key)) {
        open.add(key);
        const formula = refs.fromFileFormula(textOf(child(top.sheet.cells.get(refs.cellName(top.row, top.col)), "f")));
        for (const dep of dependencies(top.sheet, formula)) {
          if (memo.has(dep)) continue;
          if (open.has(dep)) {
            circular.add(dep);
            continue;
          }
          const [sheetName, ref] = [dep.slice(0, dep.lastIndexOf("!")), dep.slice(dep.lastIndexOf("!") + 1)];
          stack.push({ sheet: bySheet.get(sheetName), ...refs.parseCell(ref) });
        }
        continue;
      }
      compute(top.sheet, top.row, top.col);
      open.delete(key);
      stack.pop();
    }
    return memo.get(startKey);
  }

  function valueOf(sheet, row, col) {
    const ref = refs.cellName(row, col);
    const key = keyOf(sheet, row, col);
    if (memo.has(key)) return memo.get(key);
    const cell = sheet.cells.get(ref);
    const f = cell && child(cell, "f");
    if (!f || attrs(f).t === "array") return cell ? plainValue(sheet, ref) : null;
    if (open.has(key)) {
      circular.add(key);
      return 0;
    }
    return ensure(key, { sheet, row, col });
  }

  const results = [];
  for (const sheet of sheets) {
    const cols = formulaCols.get(sheet.name.toLowerCase());
    const cells = [];
    for (const [col, rows] of cols) for (const row of rows) cells.push({ row, col });
    cells.sort((a, b) => a.row - b.row || a.col - b.col);
    for (const { row, col } of cells) {
      const value = valueOf(sheet, row, col);
      results.push({ sheet, row, col, value });
    }
  }

  const errors = [];
  for (const { sheet, row, col, value } of results) {
    const ref = refs.cellName(row, col);
    const key = keyOf(sheet, row, col);
    const cell = sheet.cells.get(ref);
    const formula = refs.fromFileFormula(textOf(child(cell, "f")));
    const reason = circular.has(key) ? "circular reference" : failures.get(key);
    if (value instanceof FormulaError || reason) {
      errors.push({ sheet: sheet.name, cell: ref, formula: `=${formula}`, error: value instanceof FormulaError ? value.error : String(value), ...(reason ? { reason } : {}) });
    }
    if (!write) continue;
    const a = attrs(cell);
    removeChildren(cell, "v");
    delete a.t;
    if (uncached.has(key)) continue;
    if (value instanceof FormulaError) {
      a.t = "e";
      insertOrdered(cell, el("v", null, [textNode(value.error)]), CELL_ORDER);
    } else if (typeof value === "boolean") {
      a.t = "b";
      insertOrdered(cell, el("v", null, [textNode(value ? 1 : 0)]), CELL_ORDER);
    } else if (typeof value === "string") {
      a.t = "str";
      insertOrdered(cell, el("v", null, [textNode(value)]), CELL_ORDER);
    } else {
      insertOrdered(cell, el("v", null, [textNode(Number(value))]), CELL_ORDER);
    }
  }
  return { formulas: results.length, errors, unsupported: [...unsupported].sort() };
}

module.exports = { recalculate };
