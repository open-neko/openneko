"use strict";

const { attrs, buildXml, child, children, el, ensureChild, kids, rootOf, tagOf } = require("./ooxml.cjs");

const STYLESHEET_ORDER = ["numFmts", "fonts", "fills", "borders", "cellStyleXfs", "cellXfs", "cellStyles", "dxfs", "tableStyles", "colors", "extLst"];
const FONT_ORDER = ["b", "i", "strike", "condense", "extend", "outline", "shadow", "u", "vertAlign", "sz", "color", "name", "family", "charset", "scheme"];
const BORDER_ORDER = ["start", "left", "end", "right", "top", "bottom", "diagonal", "vertical", "horizontal"];

const BUILTIN_FORMATS = {
  General: 0, "0": 1, "0.00": 2, "#,##0": 3, "#,##0.00": 4, "0%": 9, "0.00%": 10, "0.00E+00": 11, "# ?/?": 12,
  "# ??/??": 13, "mm-dd-yy": 14, "d-mmm-yy": 15, "d-mmm": 16, "mmm-yy": 17, "h:mm AM/PM": 18, "h:mm:ss AM/PM": 19,
  "h:mm": 20, "h:mm:ss": 21, "m/d/yy h:mm": 22, "#,##0 ;(#,##0)": 37, "#,##0 ;[Red](#,##0)": 38,
  "#,##0.00;(#,##0.00)": 39, "#,##0.00;[Red](#,##0.00)": 40, "mm:ss": 45, "[h]:mm:ss": 46, "mmss.0": 47, "##0.0E+0": 48, "@": 49,
};
const BUILTIN_BY_ID = Object.fromEntries(Object.entries(BUILTIN_FORMATS).map(([code, id]) => [id, code]));
const DATE_IDS = new Set([14, 15, 16, 17, 18, 19, 20, 21, 22, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 45, 46, 47, 50, 51, 52, 53, 54, 55, 56, 57, 58]);

const STYLE_KEYS = new Set(["numFmt", "bold", "italic", "underline", "size", "color", "font", "fill", "align", "valign", "wrap", "border"]);

function isDateFormat(code) {
  const plain = String(code).replace(/"[^"]*"/g, "").replace(/\\./g, "").replace(/\[[^\]]*\]/g, "");
  return /[dmyhs]/i.test(plain) && !/^General$/i.test(plain);
}

function argb(color) {
  const hex = String(color).replace(/^#/, "").toUpperCase();
  if (/^[0-9A-F]{6}$/.test(hex)) return `FF${hex}`;
  if (/^[0-9A-F]{8}$/.test(hex)) return hex;
  throw new Error(`colour must be #RRGGBB: ${color}`);
}

function blankStylesheet() {
  return [
    el("styleSheet", { xmlns: "http://schemas.openxmlformats.org/spreadsheetml/2006/main" }, [
      el("fonts", { count: 1 }, [el("font", null, [el("sz", { val: 11 }), el("name", { val: "Calibri" }), el("family", { val: 2 })])]),
      el("fills", { count: 2 }, [el("fill", null, [el("patternFill", { patternType: "none" })]), el("fill", null, [el("patternFill", { patternType: "gray125" })])]),
      el("borders", { count: 1 }, [el("border", null, ["left", "right", "top", "bottom", "diagonal"].map((side) => el(side)))]),
      el("cellStyleXfs", { count: 1 }, [el("xf", { numFmtId: 0, fontId: 0, fillId: 0, borderId: 0 })]),
      el("cellXfs", { count: 1 }, [el("xf", { numFmtId: 0, fontId: 0, fillId: 0, borderId: 0, xfId: 0 })]),
      el("cellStyles", { count: 1 }, [el("cellStyle", { name: "Normal", xfId: 0, builtinId: 0 })]),
    ]),
  ];
}

function clone(node) {
  return JSON.parse(JSON.stringify(node));
}

function sortChildren(node, order) {
  kids(node).sort((a, b) => order.indexOf(tagOf(a)) - order.indexOf(tagOf(b)));
}

class StyleBook {
  constructor(tree) {
    this.tree = tree;
    this.root = rootOf(tree);
    this.cache = new Map();
  }

  list(name) {
    return ensureChild(this.root, name, STYLESHEET_ORDER);
  }

  xf(index) {
    return children(this.list("cellXfs"), "xf")[index] ?? children(this.list("cellXfs"), "xf")[0];
  }

  formatCode(styleIndex) {
    const xf = this.xf(Number(styleIndex) || 0);
    const id = Number(xf ? attrs(xf).numFmtId : 0) || 0;
    const custom = children(this.list("numFmts"), "numFmt").find((node) => Number(attrs(node).numFmtId) === id);
    if (custom) return attrs(custom).formatCode;
    return BUILTIN_BY_ID[id] ?? (DATE_IDS.has(id) ? "yyyy-mm-dd" : "General");
  }

  isDate(styleIndex) {
    const xf = this.xf(Number(styleIndex) || 0);
    const id = Number(xf ? attrs(xf).numFmtId : 0) || 0;
    if (DATE_IDS.has(id)) return true;
    return isDateFormat(this.formatCode(styleIndex));
  }

  append(listName, node) {
    const list = this.list(listName);
    const text = buildXml([node]);
    const existing = children(list, tagOf(node)).findIndex((other) => buildXml([other]) === text);
    if (existing >= 0) return existing;
    kids(list).push(node);
    const count = children(list, tagOf(node)).length;
    attrs(list).count = String(count);
    return count - 1;
  }

  numFmtId(code) {
    if (code in BUILTIN_FORMATS) return BUILTIN_FORMATS[code];
    const list = this.list("numFmts");
    const found = children(list, "numFmt").find((node) => attrs(node).formatCode === code);
    if (found) return Number(attrs(found).numFmtId);
    const used = children(list, "numFmt").map((node) => Number(attrs(node).numFmtId));
    const id = Math.max(163, ...used) + 1;
    kids(list).push(el("numFmt", { numFmtId: id, formatCode: code }));
    attrs(list).count = String(children(list, "numFmt").length);
    return id;
  }

  font(baseId, style) {
    const base = children(this.list("fonts"), "font")[baseId] ?? children(this.list("fonts"), "font")[0];
    const node = base ? clone(base) : el("font");
    const set = (name, on, attributes) => {
      const list = kids(node);
      const at = list.findIndex((entry) => tagOf(entry) === name);
      if (at >= 0) list.splice(at, 1);
      if (on) list.push(el(name, attributes));
    };
    if (style.bold !== undefined) set("b", style.bold);
    if (style.italic !== undefined) set("i", style.italic);
    if (style.underline !== undefined) set("u", style.underline);
    if (style.size !== undefined) set("sz", true, { val: style.size });
    if (style.color !== undefined) set("color", true, { rgb: argb(style.color) });
    if (style.font !== undefined) {
      set("name", true, { val: style.font });
      set("scheme", false);
    }
    sortChildren(node, FONT_ORDER);
    return this.append("fonts", node);
  }

  appendFill(color) {
    return this.append("fills", el("fill", null, [el("patternFill", { patternType: "solid" }, [el("fgColor", { rgb: argb(color) }), el("bgColor", { indexed: 64 })])]));
  }

  border(kind) {
    if (!kind || kind === "none") return 0;
    if (!["thin", "medium", "thick", "dashed", "dotted", "double", "hair"].includes(kind)) throw new Error(`unknown border style: ${kind}`);
    const side = (name) => el(name, { style: kind }, [el("color", { auto: 1 })]);
    const node = el("border", null, [side("left"), side("right"), side("top"), side("bottom"), el("diagonal")]);
    sortChildren(node, BORDER_ORDER);
    return this.append("borders", node);
  }

  apply(baseIndex, style) {
    for (const key of Object.keys(style)) if (!STYLE_KEYS.has(key)) throw new Error(`unknown style key: ${key}`);
    const key = `${baseIndex}|${JSON.stringify(style)}`;
    if (this.cache.has(key)) return this.cache.get(key);
    const base = this.xf(Number(baseIndex) || 0);
    const xf = base ? clone(base) : el("xf", { numFmtId: 0, fontId: 0, fillId: 0, borderId: 0, xfId: 0 });
    const a = attrs(xf);
    if (style.numFmt !== undefined) {
      a.numFmtId = String(this.numFmtId(style.numFmt));
      a.applyNumberFormat = "1";
    }
    if (["bold", "italic", "underline", "size", "color", "font"].some((name) => style[name] !== undefined)) {
      a.fontId = String(this.font(Number(a.fontId) || 0, style));
      a.applyFont = "1";
    }
    if (style.fill !== undefined) {
      a.fillId = String(style.fill === null ? 0 : this.appendFill(style.fill));
      a.applyFill = "1";
    }
    if (style.border !== undefined) {
      a.borderId = String(this.border(style.border));
      a.applyBorder = "1";
    }
    if (style.align !== undefined || style.valign !== undefined || style.wrap !== undefined) {
      let alignment = child(xf, "alignment");
      if (!alignment) {
        alignment = el("alignment");
        kids(xf).unshift(alignment);
      }
      const al = attrs(alignment);
      if (style.align !== undefined) al.horizontal = String(style.align);
      if (style.valign !== undefined) al.vertical = String(style.valign);
      if (style.wrap !== undefined) {
        if (style.wrap) al.wrapText = "1";
        else delete al.wrapText;
      }
      a.applyAlignment = "1";
    }
    const index = this.append("cellXfs", xf);
    this.cache.set(key, index);
    return index;
  }
}

module.exports = { StyleBook, blankStylesheet, isDateFormat };
