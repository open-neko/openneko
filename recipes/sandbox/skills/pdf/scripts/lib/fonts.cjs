"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { PDFHexString, PDFName, PDFNull, PDFNumber, StandardFonts } = require("pdf-lib");
const { fail } = require("./common.cjs");

const FAMILIES = {
  helvetica: [StandardFonts.Helvetica, StandardFonts.HelveticaBold, StandardFonts.HelveticaOblique, StandardFonts.HelveticaBoldOblique],
  times: [StandardFonts.TimesRoman, StandardFonts.TimesRomanBold, StandardFonts.TimesRomanItalic, StandardFonts.TimesRomanBoldItalic],
  courier: [StandardFonts.Courier, StandardFonts.CourierBold, StandardFonts.CourierOblique, StandardFonts.CourierBoldOblique],
};

async function embedFile(doc, file) {
  if (!fs.existsSync(file)) fail(`no such font file: ${file}`);
  if (!/\.(ttf|otf)$/i.test(file)) fail(`font ${path.basename(file)} must be a .ttf or .otf file`);
  if (!doc._fontkitRegistered) {
    doc.registerFontkit(require("@pdf-lib/fontkit"));
    doc._fontkitRegistered = true;
  }
  return doc.embedFont(fs.readFileSync(file), { subset: true });
}

async function family(doc, spec) {
  if (spec === undefined || spec === null) spec = "Helvetica";
  if (typeof spec === "string" && FAMILIES[spec.toLowerCase().replace(/[- ]?roman$/, "")]) {
    const names = FAMILIES[spec.toLowerCase().replace(/[- ]?roman$/, "")];
    const [regular, bold, italic, boldItalic] = await Promise.all(names.map((n) => doc.embedFont(n)));
    return { regular, bold, italic, boldItalic, standard: true };
  }
  if (typeof spec === "string") spec = { regular: spec };
  if (typeof spec !== "object" || !spec.regular) fail(`font must be Helvetica, Times, Courier, a .ttf/.otf path, or { "regular": path, "bold": path, "italic": path, "boldItalic": path }`);
  const regular = await embedFile(doc, spec.regular);
  const bold = spec.bold ? await embedFile(doc, spec.bold) : regular;
  const italic = spec.italic ? await embedFile(doc, spec.italic) : regular;
  const boldItalic = spec.boldItalic ? await embedFile(doc, spec.boldItalic) : spec.bold ? bold : italic;
  return { regular, bold, italic, boldItalic, standard: false };
}

function pick(fonts, run = {}) {
  if (run.bold && run.italic) return fonts.boldItalic;
  if (run.bold) return fonts.bold;
  if (run.italic) return fonts.italic;
  return fonts.regular;
}

const charSets = new WeakMap();

function clean(text, font) {
  let set = charSets.get(font);
  if (!set) {
    set = new Set(font.getCharacterSet());
    charSets.set(font, set);
  }
  let out = String(text).replace(/\r\n?/g, "\n").replace(/\t/g, "    ").replace(/[\u0000-\u0009\u000b-\u001f\u007f​﻿]/g, "");
  out = out.replace(/−/g, "-").replace(/[‐‑]/g, "-").replace(/ /g, " ");
  for (const ch of out) {
    if (ch === "\n") continue;
    if (!set.has(ch.codePointAt(0))) {
      const code = ch.codePointAt(0).toString(16).toUpperCase().padStart(4, "0");
      fail(`the font cannot draw "${ch}" (U+${code}). The built-in fonts hold Western European letters only. Give a .ttf or .otf font that has the character in "font"`);
    }
  }
  return out;
}

function setOutline(doc, entries) {
  if (!entries.length) return;
  const context = doc.context;
  const rootRef = context.nextRef();
  const nodes = entries.map((e) => ({ ...e, ref: context.nextRef(), children: [] }));
  const top = [];
  const stack = [];
  for (const node of nodes) {
    while (stack.length && stack[stack.length - 1].level >= node.level) stack.pop();
    if (stack.length) {
      node.parent = stack[stack.length - 1];
      node.parent.children.push(node);
    } else top.push(node);
    stack.push(node);
  }
  const pages = doc.getPages();
  const count = (list) => list.reduce((sum, n) => sum + 1 + count(n.children), 0);
  const write = (list, parentRef) => {
    list.forEach((node, i) => {
      const dict = {
        Title: PDFHexString.fromText(node.title),
        Parent: parentRef,
        Dest: context.obj([pages[node.page].ref, PDFName.of("XYZ"), PDFNull, PDFNumber.of(Math.round(node.y ?? pages[node.page].getHeight())), PDFNull]),
      };
      if (i > 0) dict.Prev = list[i - 1].ref;
      if (i < list.length - 1) dict.Next = list[i + 1].ref;
      if (node.children.length) {
        dict.First = node.children[0].ref;
        dict.Last = node.children[node.children.length - 1].ref;
        dict.Count = PDFNumber.of(node.level < 2 ? count(node.children) : -count(node.children));
        write(node.children, node.ref);
      }
      context.assign(node.ref, context.obj(dict));
    });
  };
  write(top, rootRef);
  context.assign(rootRef, context.obj({ Type: "Outlines", First: top[0].ref, Last: top[top.length - 1].ref, Count: PDFNumber.of(count(top)) }));
  doc.catalog.set(PDFName.of("Outlines"), rootRef);
  doc.catalog.set(PDFName.of("PageMode"), PDFName.of("UseOutlines"));
}

module.exports = { clean, family, pick, setOutline };
