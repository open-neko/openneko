"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { PDFHexString, PDFName, PDFNull, PDFNumber, StandardFonts, degrees } = require("pdf-lib");
const { fail } = require("./common.cjs");

const FAMILIES = {
  helvetica: [StandardFonts.Helvetica, StandardFonts.HelveticaBold, StandardFonts.HelveticaOblique, StandardFonts.HelveticaBoldOblique],
  times: [StandardFonts.TimesRoman, StandardFonts.TimesRomanBold, StandardFonts.TimesRomanItalic, StandardFonts.TimesRomanBoldItalic],
  courier: [StandardFonts.Courier, StandardFonts.CourierBold, StandardFonts.CourierOblique, StandardFonts.CourierBoldOblique],
};

const DEJAVU = "/usr/share/fonts/truetype/dejavu";
const DROID = "/usr/share/fonts-droid-fallback/truetype/DroidSansFallback.ttf";
const FALLBACK = {
  regular: [`${DEJAVU}/DejaVuSans.ttf`, DROID],
  bold: [`${DEJAVU}/DejaVuSans-Bold.ttf`, DROID],
  mono: [`${DEJAVU}/DejaVuSansMono.ttf`, DROID],
};

const styles = new WeakMap();
const chains = new WeakMap();
const embedded = new WeakMap();
const fileSets = new Map();

async function embedFile(doc, file) {
  if (!fs.existsSync(file)) fail(`no such font file: ${file}`);
  if (!/\.(ttf|otf)$/i.test(file)) fail(`font ${path.basename(file)} must be a .ttf or .otf file`);
  if (!doc._fontkitRegistered) {
    doc.registerFontkit(require("@pdf-lib/fontkit"));
    doc._fontkitRegistered = true;
  }
  return doc.embedFont(fs.readFileSync(file), { subset: true });
}

function fileSet(file) {
  if (!fileSets.has(file)) {
    let set = null;
    try {
      set = new Set(require("@pdf-lib/fontkit").create(fs.readFileSync(file)).characterSet);
    } catch {}
    fileSets.set(file, set);
  }
  return fileSets.get(file);
}

function fallbackFiles(style) {
  const key = style === "mono" ? "mono" : style === "bold" || style === "boldItalic" ? "bold" : "regular";
  return FALLBACK[key].filter((f) => fileSet(f));
}

function embedOnce(doc, file) {
  if (!embedded.has(doc)) embedded.set(doc, new Map());
  const cache = embedded.get(doc);
  if (!cache.has(file)) cache.set(file, embedFile(doc, file));
  return cache.get(file);
}

function mark(font, style) {
  if (!styles.has(font)) styles.set(font, style);
  return font;
}

async function cover(doc, font, text) {
  const own = setOf(font);
  const files = fallbackFiles(styles.get(font));
  const chain = chains.get(font) ?? [];
  const needed = new Set(chain.map((c) => c.file));
  for (const ch of String(text)) {
    const code = ch.codePointAt(0);
    if (code < 0x20 || own.has(code) || chain.some((c) => setOf(c.font).has(code))) continue;
    const file = files.find((f) => fileSet(f).has(code));
    if (file) needed.add(file);
  }
  if (needed.size === chain.length) return;
  const next = [];
  for (const file of files) if (needed.has(file)) next.push({ file, font: await embedOnce(doc, file) });
  chains.set(font, next);
}

async function fallbackFor(doc, text) {
  const chars = [...String(text)].filter((ch) => ch.codePointAt(0) >= 0x20);
  const file = fallbackFiles("regular").find((f) => chars.every((ch) => fileSet(f).has(ch.codePointAt(0))));
  return file ? embedOnce(doc, file) : null;
}

async function family(doc, spec) {
  if (spec === undefined || spec === null) spec = "Helvetica";
  if (typeof spec === "string" && FAMILIES[spec.toLowerCase().replace(/[- ]?roman$/, "")]) {
    const names = FAMILIES[spec.toLowerCase().replace(/[- ]?roman$/, "")];
    const [regular, bold, italic, boldItalic] = await Promise.all(names.map((n) => doc.embedFont(n)));
    return { regular: mark(regular, "regular"), bold: mark(bold, "bold"), italic: mark(italic, "italic"), boldItalic: mark(boldItalic, "boldItalic"), standard: true };
  }
  if (typeof spec === "string") spec = { regular: spec };
  if (typeof spec !== "object" || !spec.regular) fail(`font must be Helvetica, Times, Courier, a .ttf/.otf path, or { "regular": path, "bold": path, "italic": path, "boldItalic": path }`);
  const regular = await embedFile(doc, spec.regular);
  const bold = spec.bold ? await embedFile(doc, spec.bold) : regular;
  const italic = spec.italic ? await embedFile(doc, spec.italic) : regular;
  const boldItalic = spec.boldItalic ? await embedFile(doc, spec.boldItalic) : spec.bold ? bold : italic;
  for (const [font, style] of [[regular, "regular"], [bold, "bold"], [italic, "italic"], [boldItalic, "boldItalic"]]) mark(font, style);
  return { regular, bold, italic, boldItalic, standard: false };
}

function pick(fonts, run = {}) {
  if (run.bold && run.italic) return fonts.boldItalic;
  if (run.bold) return fonts.bold;
  if (run.italic) return fonts.italic;
  return fonts.regular;
}

const charSets = new WeakMap();

function setOf(font) {
  let set = charSets.get(font);
  if (!set) {
    set = new Set(font.getCharacterSet());
    charSets.set(font, set);
  }
  return set;
}

function parts(text, font) {
  const fonts = [font, ...(chains.get(font) ?? []).map((c) => c.font)];
  const out = [];
  for (const ch of text) {
    const code = ch.codePointAt(0);
    let use = out.length && (ch === "\n" || ch === " ") ? out[out.length - 1].font : fonts.find((f) => setOf(f).has(code));
    if (!use) {
      const hex = code.toString(16).toUpperCase().padStart(4, "0");
      fail(`the font cannot draw "${ch}" (U+${hex}), and no fallback font in the sandbox image can either. Give a .ttf or .otf font that has the character in "font"`);
    }
    if (out.length && out[out.length - 1].font === use) out[out.length - 1].text += ch;
    else out.push({ text: ch, font: use });
  }
  return out;
}

function clean(text, font) {
  let out = String(text).replace(/\r\n?/g, "\n").replace(/\t/g, "    ").replace(/[\u0000-\u0009\u000b-\u001f\u007f\u200b\ufeff]/g, "");
  out = out.replace(/\u2212/g, "-").replace(/[\u2010\u2011]/g, "-").replace(/\u202f/g, "\u00a0");
  parts(out.replace(/\n/g, ""), font);
  return out;
}

function measure(text, font, size) {
  return parts(text, font).reduce((sum, p) => sum + p.font.widthOfTextAtSize(p.text, size), 0);
}

function draw(page, text, { font, size, x, y, angle, ...rest }) {
  const rad = ((angle ?? 0) * Math.PI) / 180;
  for (const p of parts(text, font)) {
    page.drawText(p.text, { ...rest, x, y, font: p.font, size, ...(angle ? { rotate: degrees(angle) } : {}) });
    const w = p.font.widthOfTextAtSize(p.text, size);
    x += w * Math.cos(rad);
    y += w * Math.sin(rad);
  }
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

module.exports = { clean, cover, draw, fallbackFor, family, mark, measure, pick, setOutline };
