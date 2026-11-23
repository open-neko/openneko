"use strict";

const MAX_ROW = 1048576;
const MAX_COL = 16384;

function colToNumber(letters) {
  let n = 0;
  for (const ch of letters.toUpperCase()) n = n * 26 + (ch.charCodeAt(0) - 64);
  return n;
}

function numberToCol(n) {
  let out = "";
  while (n > 0) {
    const rem = (n - 1) % 26;
    out = String.fromCharCode(65 + rem) + out;
    n = Math.floor((n - 1) / 26);
  }
  return out;
}

function parseCell(ref) {
  const match = /^\$?([A-Za-z]{1,3})\$?(\d+)$/.exec(String(ref).trim());
  if (!match) throw new Error(`not a cell reference: ${ref}`);
  const col = colToNumber(match[1]);
  const row = Number(match[2]);
  if (col < 1 || col > MAX_COL || row < 1 || row > MAX_ROW) throw new Error(`cell reference out of range: ${ref}`);
  return { row, col };
}

function cellName(row, col) {
  return `${numberToCol(col)}${row}`;
}

function parseRange(ref) {
  const text = String(ref).trim();
  const [a, b] = text.split(":");
  if (b === undefined) {
    const one = parseCell(a);
    return { from: one, to: { ...one } };
  }
  if (/^\$?[A-Za-z]{1,3}$/.test(a) && /^\$?[A-Za-z]{1,3}$/.test(b)) {
    return { from: { row: 1, col: colToNumber(a.replace("$", "")) }, to: { row: MAX_ROW, col: colToNumber(b.replace("$", "")) } };
  }
  if (/^\$?\d+$/.test(a) && /^\$?\d+$/.test(b)) {
    return { from: { row: Number(a.replace("$", "")), col: 1 }, to: { row: Number(b.replace("$", "")), col: MAX_COL } };
  }
  const from = parseCell(a);
  const to = parseCell(b);
  return {
    from: { row: Math.min(from.row, to.row), col: Math.min(from.col, to.col) },
    to: { row: Math.max(from.row, to.row), col: Math.max(from.col, to.col) },
  };
}

function rangeName(range) {
  const a = cellName(range.from.row, range.from.col);
  const b = cellName(range.to.row, range.to.col);
  return a === b ? a : `${a}:${b}`;
}

function quoteSheet(name) {
  if (/^[A-Za-z_][A-Za-z0-9_]*$/.test(name) && !/^[A-Za-z]{1,3}\d+$/.test(name) && !/^(R|C|RC)\d*$/i.test(name)) return name;
  return `'${name.replace(/'/g, "''")}'`;
}

function unquoteSheet(token) {
  return token.startsWith("'") ? token.slice(1, -1).replace(/''/g, "'") : token;
}

const SHEET_PREFIX = String.raw`(?:'(?:[^']|'')+'|[A-Za-z_][A-Za-z0-9_.]*)!`;
const CELL = String.raw`\$?[A-Za-z]{1,3}\$?\d+`;
const COLS = String.raw`\$?[A-Za-z]{1,3}:\$?[A-Za-z]{1,3}`;
const ROWS = String.raw`\$?\d+:\$?\d+`;
const TOKEN = new RegExp(
  String.raw`("(?:[^"]|"")*")|(${SHEET_PREFIX})?(${COLS}|${ROWS}|${CELL}(?::${CELL})?)(?![A-Za-z0-9_(.!])|(${SHEET_PREFIX})`,
  "g",
);

function scan(formula, onRef, onSheetOnly) {
  return formula.replace(TOKEN, (whole, str, sheet, ref, bareSheet, offset, all) => {
    if (str !== undefined) return whole;
    if (bareSheet !== undefined) return onSheetOnly ? onSheetOnly(bareSheet) : whole;
    const before = offset > 0 ? all[offset - 1] : "";
    if (/[A-Za-z0-9_.$]/.test(before)) return whole;
    return onRef(sheet ?? "", ref);
  });
}

function shiftPart(part, dRow, dCol) {
  return part.replace(/(\$?)([A-Za-z]{1,3})?(\$?)(\d+)?/, (whole, colAbs, col, rowAbs, row) => {
    let out = "";
    if (!col && colAbs) rowAbs = "$";
    if (col) out += colAbs ? `$${col}` : numberToCol(Math.max(1, colToNumber(col) + dCol));
    if (row) out += rowAbs ? `$${row}` : String(Math.max(1, Number(row) + dRow));
    return out;
  });
}

function shiftFormula(formula, dRow, dCol) {
  return scan(formula, (sheet, ref) => sheet + ref.split(":").map((part) => shiftPart(part, dRow, dCol)).join(":"));
}

function renameSheetInFormula(formula, from, to) {
  const replace = (prefix) => (unquoteSheet(prefix.slice(0, -1)).toLowerCase() === from.toLowerCase() ? `${quoteSheet(to)}!` : prefix);
  return scan(formula, (sheet, ref) => (sheet ? replace(sheet) : "") + ref, replace);
}

function sheetsInFormula(formula) {
  const found = new Set();
  const note = (prefix) => {
    found.add(unquoteSheet(prefix.slice(0, -1)).toLowerCase());
    return prefix;
  };
  scan(formula, (sheet, ref) => {
    if (sheet) note(sheet);
    return ref;
  }, note);
  return found;
}

const FUTURE_FUNCTIONS = [
  "AGGREGATE", "ARABIC", "BASE", "BITAND", "BITLSHIFT", "BITOR", "BITRSHIFT", "BITXOR", "CEILING.MATH", "CEILING.PRECISE",
  "CHISQ.DIST", "CHISQ.INV", "COMBINA", "CONCAT", "COVARIANCE.P", "COVARIANCE.S", "DAYS", "DECIMAL", "ERF.PRECISE",
  "EXPON.DIST", "F.DIST", "F.INV", "FLOOR.MATH", "FLOOR.PRECISE", "FORECAST.LINEAR", "FORMULATEXT", "GAMMA", "GAMMA.DIST",
  "GAMMA.INV", "IFNA", "IFS", "ISFORMULA", "ISOWEEKNUM", "LOGNORM.DIST", "LOGNORM.INV", "MAXIFS", "MINIFS",
  "MODE.MULT", "MODE.SNGL", "NETWORKDAYS.INTL", "NORM.DIST", "NORM.INV", "NORM.S.DIST", "NORM.S.INV", "NUMBERVALUE",
  "PERCENTILE.EXC", "PERCENTILE.INC", "POISSON.DIST", "QUARTILE.EXC", "QUARTILE.INC", "RANK.AVG", "RANK.EQ", "SHEET",
  "SHEETS", "STDEV.P", "STDEV.S", "SWITCH", "T.DIST", "T.DIST.2T", "T.DIST.RT", "T.INV", "T.INV.2T", "TEXTJOIN",
  "UNICHAR", "UNICODE", "VAR.P", "VAR.S", "WEIBULL.DIST", "WORKDAY.INTL", "XLOOKUP", "XMATCH", "XOR",
];
const FUTURE_PATTERN = new RegExp(
  String.raw`("(?:[^"]|"")*")|(?<![A-Za-z0-9_.])(${FUTURE_FUNCTIONS.map((name) => name.replace(/\./g, "\\.")).join("|")})(?=\s*\()`,
  "gi",
);

function toFileFormula(formula) {
  const body = String(formula).replace(/^=/, "");
  return body.replace(FUTURE_PATTERN, (whole, str, name) => (str !== undefined ? whole : `_xlfn.${name.toUpperCase()}`));
}

function fromFileFormula(formula) {
  return String(formula).replace(/("(?:[^"]|"")*")|_xlfn\.(?:_xlws\.)?/g, (whole, str) => str ?? "");
}

module.exports = {
  MAX_COL,
  MAX_ROW,
  cellName,
  colToNumber,
  fromFileFormula,
  numberToCol,
  parseCell,
  parseRange,
  quoteSheet,
  rangeName,
  renameSheetInFormula,
  sheetsInFormula,
  shiftFormula,
  toFileFormula,
  unquoteSheet,
};
