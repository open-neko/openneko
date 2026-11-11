"use strict";

const { strToU8 } = require("fflate");
const {
  Package,
  attrs,
  child,
  children,
  el,
  ensureChild,
  insertOrdered,
  kids,
  removeChildren,
  rootOf,
  tagOf,
  textNode,
  textOf,
} = require("./ooxml.cjs");
const refs = require("./refs.cjs");
const { StyleBook, blankStylesheet } = require("./styles.cjs");

const NS = {
  main: "http://schemas.openxmlformats.org/spreadsheetml/2006/main",
  rel: "http://schemas.openxmlformats.org/officeDocument/2006/relationships",
  officeDocument: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument",
  worksheet: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet",
  styles: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles",
  sharedStrings: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings",
  calcChain: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/calcChain",
};
const CONTENT = {
  workbook: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml",
  worksheet: "application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml",
  styles: "application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml",
};

const WORKBOOK_ORDER = ["fileVersion", "fileSharing", "workbookPr", "workbookProtection", "bookViews", "sheets", "functionGroups", "externalReferences", "definedNames", "calcPr", "oleSize", "customWorkbookViews", "pivotCaches", "smartTagPr", "smartTagTypes", "webPublishing", "fileRecoveryPr", "webPublishObjects", "extLst"];
const WORKSHEET_ORDER = ["sheetPr", "dimension", "sheetViews", "sheetFormatPr", "cols", "sheetData", "sheetCalcPr", "sheetProtection", "protectedRanges", "scenarios", "autoFilter", "sortState", "dataConsolidate", "customSheetViews", "mergeCells", "phoneticPr", "conditionalFormatting", "dataValidations", "hyperlinks", "printOptions", "pageMargins", "pageSetup", "headerFooter", "rowBreaks", "colBreaks", "customProperties", "cellWatches", "ignoredErrors", "smartTags", "drawing", "legacyDrawing", "legacyDrawingHF", "picture", "oleObjects", "controls", "webPublishItems", "tableParts", "extLst"];
const CELL_ORDER = ["f", "v", "is", "extLst"];
const INVALID_SHEET_CHARS = /[\\/?*[\]:]/;

function blankPackage() {
  const xml = (text) => strToU8(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n${text}`);
  const files = new Map();
  files.set("[Content_Types].xml", xml(
    `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="${CONTENT.workbook}"/><Override PartName="/xl/styles.xml" ContentType="${CONTENT.styles}"/></Types>`,
  ));
  files.set("_rels/.rels", xml(
    `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${NS.officeDocument}" Target="xl/workbook.xml"/></Relationships>`,
  ));
  files.set("xl/workbook.xml", xml(
    `<workbook xmlns="${NS.main}" xmlns:r="${NS.rel}"><bookViews><workbookView/></bookViews><sheets/><calcPr calcId="191029" fullCalcOnLoad="1"/></workbook>`,
  ));
  files.set("xl/_rels/workbook.xml.rels", xml(
    `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${NS.styles}" Target="styles.xml"/></Relationships>`,
  ));
  const pkg = new Package(files);
  pkg.setXml("xl/styles.xml", blankStylesheet());
  return pkg;
}

function excelSerial(iso, date1904) {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2}):(\d{2})(?::(\d{2}))?)?$/.exec(String(iso));
  if (!match) throw new Error(`date must be YYYY-MM-DD or YYYY-MM-DDTHH:MM[:SS]: ${iso}`);
  const [, y, m, d, hh = "0", mm = "0", ss = "0"] = match;
  const ms = Date.UTC(Number(y), Number(m) - 1, Number(d), Number(hh), Number(mm), Number(ss));
  const epoch = date1904 ? Date.UTC(1904, 0, 1) : Date.UTC(1899, 11, 30);
  return (ms - epoch) / 86400000;
}

function serialToIso(serial, date1904) {
  const epoch = date1904 ? Date.UTC(1904, 0, 1) : Date.UTC(1899, 11, 30);
  const ms = Math.round(serial * 86400) * 1000 + epoch;
  const iso = new Date(ms).toISOString();
  return Number.isInteger(serial) ? iso.slice(0, 10) : iso.slice(0, 19);
}

class Sheet {
  constructor(book, info) {
    this.book = book;
    this.name = info.name;
    this.path = info.path;
    this.info = info;
    this.tree = book.pkg.xml(info.path);
    if (!this.tree) throw new Error(`sheet part is missing: ${info.path}`);
    this.root = rootOf(this.tree);
    this.data = ensureChild(this.root, "sheetData", WORKSHEET_ORDER);
    this.index();
  }

  index() {
    this.rows = new Map();
    this.cells = new Map();
    let rowNumber = 0;
    for (const row of children(this.data, "row")) {
      const a = attrs(row);
      rowNumber = a.r ? Number(a.r) : rowNumber + 1;
      a.r = String(rowNumber);
      this.rows.set(rowNumber, row);
      let col = 0;
      for (const cell of children(row, "c")) {
        const ca = attrs(cell);
        col = ca.r ? refs.parseCell(ca.r).col : col + 1;
        ca.r = refs.cellName(rowNumber, col);
        this.cells.set(ca.r, cell);
      }
    }
  }

  expandSharedFormulas() {
    const masters = new Map();
    for (const [ref, cell] of this.cells) {
      const f = child(cell, "f");
      if (!f || attrs(f).t !== "shared" || !textOf(f)) continue;
      masters.set(attrs(f).si, { at: refs.parseCell(ref), formula: textOf(f) });
    }
    for (const [ref, cell] of this.cells) {
      const f = child(cell, "f");
      if (!f || attrs(f).t !== "shared") continue;
      const master = masters.get(attrs(f).si);
      const at = refs.parseCell(ref);
      const formula = master ? refs.shiftFormula(master.formula, at.row - master.at.row, at.col - master.at.col) : textOf(f);
      cell[tagOf(cell)][kids(cell).indexOf(f)] = el("f", null, [textNode(formula)]);
    }
  }

  extent() {
    let maxRow = 0;
    let maxCol = 0;
    let minRow = Infinity;
    let minCol = Infinity;
    for (const [ref, cell] of this.cells) {
      if (!child(cell, "v") && !child(cell, "f") && !child(cell, "is")) continue;
      const { row, col } = refs.parseCell(ref);
      maxRow = Math.max(maxRow, row);
      maxCol = Math.max(maxCol, col);
      minRow = Math.min(minRow, row);
      minCol = Math.min(minCol, col);
    }
    if (!maxRow) return null;
    return { from: { row: minRow, col: minCol }, to: { row: maxRow, col: maxCol } };
  }

  cell(ref) {
    const node = this.cells.get(ref);
    if (!node) return { ref, type: "empty", value: null };
    const a = attrs(node);
    const f = child(node, "f");
    const v = child(node, "v");
    const out = { ref, type: "empty", value: null, style: Number(a.s) || 0 };
    if (f) out.formula = refs.fromFileFormula(textOf(f));
    if (f && attrs(f).t === "array") out.arrayFormula = attrs(f).ref;
    switch (a.t) {
      case "s": {
        out.type = "string";
        out.value = this.book.sharedString(Number(textOf(v)));
        break;
      }
      case "inlineStr":
        out.type = "string";
        out.value = textOf(child(node, "is"));
        break;
      case "str":
        out.type = "string";
        out.value = v ? textOf(v) : "";
        break;
      case "b":
        out.type = "boolean";
        out.value = textOf(v) === "1";
        break;
      case "e":
        out.type = "error";
        out.value = textOf(v);
        break;
      default:
        if (v && textOf(v) !== "") {
          out.type = "number";
          out.value = Number(textOf(v));
          if (this.book.styles.isDate(out.style)) {
            out.type = "date";
            out.serial = out.value;
            out.value = serialToIso(out.value, this.book.date1904);
          }
        }
    }
    if (f && !v && out.type === "empty") out.type = "formula";
    return out;
  }

  rowNode(rowNumber) {
    let row = this.rows.get(rowNumber);
    if (row) return row;
    row = el("row", { r: rowNumber });
    const list = kids(this.data);
    let at = list.length;
    for (let i = 0; i < list.length; i++) {
      if (tagOf(list[i]) === "row" && Number(attrs(list[i]).r) > rowNumber) {
        at = i;
        break;
      }
    }
    list.splice(at, 0, row);
    this.rows.set(rowNumber, row);
    return row;
  }

  cellNode(ref) {
    let cell = this.cells.get(ref);
    if (cell) return cell;
    const { row, col } = refs.parseCell(ref);
    const rowNode = this.rowNode(row);
    cell = el("c", { r: ref });
    const list = kids(rowNode);
    let at = list.length;
    for (let i = 0; i < list.length; i++) {
      if (tagOf(list[i]) === "c" && refs.parseCell(attrs(list[i]).r).col > col) {
        at = i;
        break;
      }
    }
    list.splice(at, 0, cell);
    const ra = attrs(rowNode);
    delete ra.spans;
    this.cells.set(ref, cell);
    return cell;
  }

  set(ref, input) {
    const at = refs.parseCell(ref);
    const name = refs.cellName(at.row, at.col);
    let spec = input;
    if (spec === null || typeof spec !== "object" || Array.isArray(spec)) spec = { value: spec };
    if (Array.isArray(spec.value)) throw new Error(`cell ${name}: a value cannot be a list`);
    const touchesContent = ["value", "formula", "date", "text"].some((key) => key in spec);
    if (!touchesContent && !spec.style) return;
    const cell = this.cellNode(name);
    const a = attrs(cell);
    let style = spec.style ? { ...spec.style } : null;
    if (touchesContent) {
      for (const tag of ["f", "v", "is"]) removeChildren(cell, tag);
      delete a.t;
      let formula = spec.formula;
      let value = spec.value;
      if ("text" in spec) value = String(spec.text);
      else if (formula === undefined && typeof value === "string" && value.startsWith("=") && value.length > 1) formula = value;
      if ("date" in spec) {
        value = excelSerial(spec.date, this.book.date1904);
        if (!style?.numFmt && !this.book.styles.isDate(a.s)) style = { ...(style ?? {}), numFmt: /T|\s/.test(spec.date) ? "yyyy-mm-dd hh:mm" : "yyyy-mm-dd" };
      }
      if (formula !== undefined && formula !== null) {
        insertOrdered(cell, el("f", null, [textNode(refs.toFileFormula(formula))]), CELL_ORDER);
        this.book.formulasChanged = true;
      } else if (typeof value === "number") {
        if (!Number.isFinite(value)) throw new Error(`cell ${name}: numbers must be finite`);
        insertOrdered(cell, el("v", null, [textNode(value)]), CELL_ORDER);
      } else if (typeof value === "boolean") {
        a.t = "b";
        insertOrdered(cell, el("v", null, [textNode(value ? 1 : 0)]), CELL_ORDER);
      } else if (typeof value === "string") {
        if (value.length > 32767) throw new Error(`cell ${name}: text is longer than 32,767 characters`);
        a.t = "inlineStr";
        insertOrdered(cell, el("is", null, [el("t", { "xml:space": "preserve" }, [textNode(value)])]), CELL_ORDER);
      } else if (value !== null && value !== undefined) {
        throw new Error(`cell ${name}: unsupported value ${JSON.stringify(value)}`);
      }
      this.book.valuesChanged = true;
    }
    if (style) {
      const index = this.book.styles.apply(Number(a.s) || 0, style);
      if (index) a.s = String(index);
      else delete a.s;
    }
  }

  setColumnWidth(col, width) {
    const cols = ensureChild(this.root, "cols", WORKSHEET_ORDER);
    const list = kids(cols);
    for (let i = list.length - 1; i >= 0; i--) {
      const a = attrs(list[i]);
      const min = Number(a.min);
      const max = Number(a.max);
      if (col < min || col > max) continue;
      list.splice(i, 1);
      if (min < col) list.push(el("col", { ...a, min, max: col - 1 }));
      if (col < max) list.push(el("col", { ...a, min: col + 1, max }));
    }
    list.push(el("col", { min: col, max: col, width, customWidth: 1 }));
    list.sort((x, y) => Number(attrs(x).min) - Number(attrs(y).min));
  }

  freeze(ref) {
    const views = ensureChild(this.root, "sheetViews", WORKSHEET_ORDER);
    let view = child(views, "sheetView");
    if (!view) {
      view = el("sheetView", { workbookViewId: 0 });
      kids(views).push(view);
    }
    removeChildren(view, "pane");
    removeChildren(view, "selection");
    if (!ref || ref === "A1") return;
    const { row, col } = refs.parseCell(ref);
    const xSplit = col - 1;
    const ySplit = row - 1;
    const activePane = xSplit && ySplit ? "bottomRight" : ySplit ? "bottomLeft" : "topRight";
    kids(view).unshift(
      el("pane", { xSplit: xSplit || undefined, ySplit: ySplit || undefined, topLeftCell: ref, activePane, state: "frozen" }),
      el("selection", { pane: activePane, activeCell: ref, sqref: ref }),
    );
  }

  merge(range) {
    const text = refs.rangeName(refs.parseRange(range));
    const merges = ensureChild(this.root, "mergeCells", WORKSHEET_ORDER);
    if (!children(merges, "mergeCell").some((node) => attrs(node).ref === text)) kids(merges).push(el("mergeCell", { ref: text }));
    attrs(merges).count = String(children(merges, "mergeCell").length);
  }

  finish() {
    const extent = this.extent();
    const dimension = ensureChild(this.root, "dimension", WORKSHEET_ORDER);
    attrs(dimension).ref = extent ? refs.rangeName(extent) : "A1";
    for (const row of children(this.data, "row")) delete attrs(row).spans;
  }
}

class Workbook {
  constructor(pkg) {
    this.pkg = pkg;
    const main = pkg.rels("").find((rel) => rel.type === NS.officeDocument);
    if (!main) throw new Error("the package has no workbook part");
    this.path = main.part;
    this.tree = pkg.xml(this.path);
    this.root = rootOf(this.tree);
    if (tagOf(this.root) !== "workbook") throw new Error("the main part is not a spreadsheet workbook");
    const pr = child(this.root, "workbookPr");
    this.date1904 = pr ? ["1", "true"].includes(attrs(pr).date1904) : false;
    let stylesRel = pkg.rels(this.path).find((rel) => rel.type === NS.styles);
    if (!stylesRel) {
      pkg.addRel(this.path, NS.styles, "styles.xml");
      pkg.setXml("xl/styles.xml", blankStylesheet());
      pkg.setOverride("xl/styles.xml", CONTENT.styles);
      stylesRel = pkg.rels(this.path).find((rel) => rel.type === NS.styles);
    }
    this.styles = new StyleBook(pkg.xml(stylesRel.part));
    this.loaded = new Map();
    this.strings = null;
  }

  static open(bytes) {
    return new Workbook(Package.open(bytes));
  }

  static blank() {
    return new Workbook(blankPackage());
  }

  sheetInfos() {
    const rels = new Map(this.pkg.rels(this.path).map((rel) => [rel.id, rel]));
    const sheets = child(this.root, "sheets");
    return (sheets ? children(sheets, "sheet") : []).map((node, position) => {
      const a = attrs(node);
      const rel = rels.get(a["r:id"]);
      return { name: a.name, sheetId: a.sheetId, relId: a["r:id"], state: a.state ?? "visible", path: rel?.part, worksheet: rel?.type === NS.worksheet, node, position };
    });
  }

  sheetNames() {
    return this.sheetInfos().filter((info) => info.worksheet).map((info) => info.name);
  }

  sheet(name) {
    const infos = this.sheetInfos().filter((info) => info.worksheet);
    const info = name === undefined ? infos[0] : infos.find((entry) => entry.name.toLowerCase() === String(name).toLowerCase());
    if (!info) throw new Error(name === undefined ? "the workbook has no worksheet" : `no sheet named "${name}". Sheets: ${infos.map((entry) => entry.name).join(", ")}`);
    if (!this.loaded.has(info.path)) this.loaded.set(info.path, new Sheet(this, info));
    return this.loaded.get(info.path);
  }

  sheets() {
    return this.sheetNames().map((name) => this.sheet(name));
  }

  sharedString(index) {
    if (!this.strings) {
      this.strings = [];
      const rel = this.pkg.rels(this.path).find((entry) => entry.type === NS.sharedStrings);
      const tree = rel ? this.pkg.xml(rel.part) : undefined;
      if (tree) {
        for (const si of children(rootOf(tree), "si")) {
          const direct = child(si, "t");
          this.strings.push(direct ? textOf(direct) : children(si, "r").map((run) => textOf(child(run, "t"))).join(""));
        }
      }
    }
    return this.strings[index] ?? "";
  }

  checkSheetName(name, ignore) {
    if (typeof name !== "string" || !name.trim()) throw new Error("a sheet name cannot be empty");
    if (name.length > 31) throw new Error(`sheet name is longer than 31 characters: ${name}`);
    if (INVALID_SHEET_CHARS.test(name) || name.startsWith("'") || name.endsWith("'")) throw new Error(`sheet name has a character Excel refuses: ${name}`);
    if (name.toLowerCase() === "history") throw new Error("Excel reserves the sheet name History");
    const clash = this.sheetInfos().find((info) => info.name.toLowerCase() === name.toLowerCase() && info.name !== ignore);
    if (clash) throw new Error(`a sheet named "${clash.name}" already exists`);
  }

  addSheet(name) {
    this.checkSheetName(name);
    const used = new Set([...this.pkg.files.keys()]);
    let n = 1;
    while (used.has(`xl/worksheets/sheet${n}.xml`)) n++;
    const part = `xl/worksheets/sheet${n}.xml`;
    this.pkg.setXml(part, [el("worksheet", { xmlns: NS.main, "xmlns:r": NS.rel }, [el("dimension", { ref: "A1" }), el("sheetViews", null, [el("sheetView", { workbookViewId: 0 })]), el("sheetFormatPr", { defaultRowHeight: 15 }), el("sheetData"), el("pageMargins", { left: 0.7, right: 0.7, top: 0.75, bottom: 0.75, header: 0.3, footer: 0.3 })])]);
    this.pkg.setOverride(part, CONTENT.worksheet);
    const target = this.path.includes("/") ? part.slice(this.path.lastIndexOf("/") + 1) : part;
    const relId = this.pkg.addRel(this.path, NS.worksheet, target);
    const sheets = ensureChild(this.root, "sheets", WORKBOOK_ORDER);
    const ids = children(sheets, "sheet").map((node) => Number(attrs(node).sheetId) || 0);
    kids(sheets).push(el("sheet", { name, sheetId: Math.max(0, ...ids) + 1, "r:id": relId }));
    return this.sheet(name);
  }

  formulaSites() {
    const sites = [];
    for (const sheet of this.sheets()) {
      sheet.expandSharedFormulas();
      for (const cell of sheet.cells.values()) {
        const f = child(cell, "f");
        if (f) sites.push({ get: () => textOf(f), set: (text) => { kids(f).splice(0, kids(f).length, textNode(text)); } });
      }
    }
    const names = child(this.root, "definedNames");
    for (const node of names ? children(names, "definedName") : []) {
      sites.push({ get: () => textOf(node), set: (text) => { kids(node).splice(0, kids(node).length, textNode(text)); }, name: attrs(node).name });
    }
    return sites;
  }

  renameSheet(from, to) {
    const info = this.sheetInfos().find((entry) => entry.name.toLowerCase() === String(from).toLowerCase());
    if (!info) throw new Error(`no sheet named "${from}"`);
    this.checkSheetName(to, info.name);
    for (const site of this.formulaSites()) {
      const before = site.get();
      const after = refs.renameSheetInFormula(before, info.name, to);
      if (after !== before) site.set(after);
    }
    attrs(info.node).name = to;
    const loaded = this.loaded.get(info.path);
    if (loaded) loaded.name = to;
    this.formulasChanged = true;
  }

  deleteSheet(name) {
    const infos = this.sheetInfos();
    const info = infos.find((entry) => entry.name.toLowerCase() === String(name).toLowerCase());
    if (!info) throw new Error(`no sheet named "${name}"`);
    if (infos.length === 1) throw new Error("a workbook needs at least one sheet");
    const target = info.name.toLowerCase();
    for (const other of this.sheets()) {
      if (other.path === info.path) continue;
      for (const [ref, cell] of other.cells) {
        const f = child(cell, "f");
        if (f && refs.sheetsInFormula(textOf(f)).has(target)) throw new Error(`cannot delete "${info.name}": ${other.name}!${ref} refers to it`);
      }
    }
    const names = child(this.root, "definedNames");
    if (names) {
      for (const node of [...children(names, "definedName")]) {
        const a = attrs(node);
        if (a.localSheetId !== undefined && Number(a.localSheetId) === info.position) {
          kids(names).splice(kids(names).indexOf(node), 1);
          continue;
        }
        if (refs.sheetsInFormula(textOf(node)).has(target)) throw new Error(`cannot delete "${info.name}": the defined name ${a.name} refers to it`);
        if (a.localSheetId !== undefined && Number(a.localSheetId) > info.position) a.localSheetId = String(Number(a.localSheetId) - 1);
      }
      if (!children(names, "definedName").length) removeChildren(this.root, "definedNames");
    }
    const sheets = child(this.root, "sheets");
    kids(sheets).splice(kids(sheets).indexOf(info.node), 1);
    this.pkg.removeRel(this.path, info.relId);
    this.pkg.setOverride(info.path, null);
    const sheetRels = info.path.replace(/([^/]+)$/, "_rels/$1.rels");
    this.pkg.remove(info.path);
    this.pkg.remove(sheetRels);
    this.loaded.delete(info.path);
    const views = child(this.root, "bookViews");
    for (const view of views ? children(views, "workbookView") : []) {
      delete attrs(view).activeTab;
      delete attrs(view).firstSheet;
    }
    this.formulasChanged = true;
  }

  defineName(name, ref, sheet) {
    if (!/^[A-Za-z_\\][A-Za-z0-9_.]*$/.test(name) || /^[A-Za-z]{1,3}\d+$/.test(name)) throw new Error(`not a valid defined name: ${name}`);
    const names = ensureChild(this.root, "definedNames", WORKBOOK_ORDER);
    const localSheetId = sheet === undefined ? undefined : this.sheetInfos().findIndex((info) => info.name.toLowerCase() === String(sheet).toLowerCase());
    if (localSheetId === -1) throw new Error(`no sheet named "${sheet}"`);
    for (const node of [...children(names, "definedName")]) {
      const a = attrs(node);
      if (a.name.toLowerCase() === name.toLowerCase() && String(a.localSheetId ?? "") === String(localSheetId ?? "")) kids(names).splice(kids(names).indexOf(node), 1);
    }
    kids(names).push(el("definedName", { name, localSheetId }, [textNode(refs.toFileFormula(ref))]));
    this.formulasChanged = true;
  }

  definedNames() {
    const names = child(this.root, "definedNames");
    const infos = this.sheetInfos();
    return (names ? children(names, "definedName") : []).map((node) => {
      const a = attrs(node);
      return { name: a.name, ref: refs.fromFileFormula(textOf(node)), ...(a.localSheetId !== undefined ? { sheet: infos[Number(a.localSheetId)]?.name } : {}), ...(a.hidden === "1" ? { hidden: true } : {}) };
    });
  }

  dropCalcChain() {
    const rel = this.pkg.rels(this.path).find((entry) => entry.type === NS.calcChain);
    if (!rel) return;
    this.pkg.removeRel(this.path, rel.id);
    this.pkg.setOverride(rel.part, null);
    this.pkg.remove(rel.part);
  }

  bytes() {
    if (!this.sheetNames().length) throw new Error("a workbook needs at least one sheet");
    for (const sheet of this.loaded.values()) sheet.finish();
    if (this.formulasChanged || this.valuesChanged) this.dropCalcChain();
    attrs(ensureChild(this.root, "calcPr", WORKBOOK_ORDER)).fullCalcOnLoad = "1";
    return this.pkg.bytes();
  }
}

module.exports = { Workbook, Sheet, excelSerial, serialToIso };
