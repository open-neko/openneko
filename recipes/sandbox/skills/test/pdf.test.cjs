"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const zlib = require("node:zlib");
const { execFileSync, spawnSync } = require("node:child_process");
const { PDFDocument, PDFHexString, PDFName, PDFString, StandardFonts } = require("pdf-lib");
const { markdownBlocks, parseInline } = require("../pdf/scripts/lib/build.cjs");
const { parseRanges } = require("../pdf/scripts/lib/common.cjs");

const CLI = path.join(__dirname, "..", "pdf", "scripts", "pdf.cjs");
const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pdf-skill-"));
test.after(() => fs.rmSync(dir, { recursive: true, force: true }));

function cli(...args) {
  return execFileSync(process.execPath, [CLI, ...args], { encoding: "utf8", stdio: ["pipe", "pipe", "pipe"] });
}

function json(...args) {
  return JSON.parse(cli(...args));
}

function cliFails(...args) {
  const result = spawnSync(process.execPath, [CLI, ...args], { encoding: "utf8" });
  assert.notEqual(result.status, 0, `expected a failure, got: ${result.stdout}`);
  assert.equal(result.stderr.includes("    at "), false, `expected a message, got a stack: ${result.stderr}`);
  return result.stderr;
}

function file(name) {
  return path.join(dir, name);
}

function writeJson(name, value) {
  fs.writeFileSync(file(name), JSON.stringify(value));
  return file(name);
}

function has(binary) {
  return spawnSync("sh", ["-c", `command -v ${binary}`]).status === 0;
}

function python(module) {
  return spawnSync("python3", ["-c", `import ${module}`]).status === 0;
}

function png(width, height) {
  const chunk = (type, data) => {
    const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
    const length = Buffer.alloc(4);
    length.writeUInt32BE(data.length);
    const crc = Buffer.alloc(4);
    crc.writeUInt32BE(zlib.crc32(body) >>> 0);
    return Buffer.concat([length, body, crc]);
  };
  const header = Buffer.alloc(13);
  header.writeUInt32BE(width, 0);
  header.writeUInt32BE(height, 4);
  header[8] = 8;
  header[9] = 2;
  const rows = Buffer.alloc((width * 3 + 1) * height, 0x80);
  for (let y = 0; y < height; y++) rows[y * (width * 3 + 1)] = 0;
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk("IHDR", header), chunk("IDAT", zlib.deflateSync(rows)), chunk("IEND", Buffer.alloc(0))]);
}

async function load(name) {
  return PDFDocument.load(fs.readFileSync(file(name)));
}

async function textPdf(name, pages) {
  const doc = await PDFDocument.create();
  const font = await doc.embedFont(StandardFonts.Helvetica);
  for (const text of pages) doc.addPage([595, 842]).drawText(text, { x: 60, y: 760, size: 14, font });
  fs.writeFileSync(file(name), await doc.save());
  return file(name);
}

async function formPdf(name) {
  const doc = await PDFDocument.create();
  const page = doc.addPage([595, 842]);
  const font = await doc.embedFont(StandardFonts.Helvetica);
  page.drawText("Supplier form", { x: 50, y: 790, size: 16, font });
  const form = doc.getForm();
  form.createTextField("supplier.name").addToPage(page, { x: 50, y: 700, width: 200, height: 20 });
  const code = form.createTextField("code");
  code.setMaxLength(4);
  code.addToPage(page, { x: 50, y: 660, width: 100, height: 20 });
  form.createCheckBox("approved").addToPage(page, { x: 50, y: 620 });
  const tier = form.createRadioGroup("tier");
  tier.addOptionToPage("gold", page, { x: 50, y: 580 });
  tier.addOptionToPage("silver", page, { x: 100, y: 580 });
  const region = form.createDropdown("region");
  region.addOptions(["EMEA", "APAC"]);
  region.addToPage(page, { x: 50, y: 540 });
  const tags = form.createOptionList("tags");
  tags.addOptions(["a", "b", "c"]);
  tags.enableMultiselect();
  tags.addToPage(page, { x: 50, y: 440, height: 80 });
  const locked = form.createTextField("locked");
  locked.setText("fixed");
  locked.enableReadOnly();
  locked.addToPage(page, { x: 300, y: 700 });
  const second = doc.addPage([595, 842]);
  second.drawText("Page two", { x: 50, y: 790, size: 16, font });
  form.createTextField("notes").addToPage(second, { x: 50, y: 700, width: 300, height: 20 });
  fs.writeFileSync(file(name), await doc.save());
  return file(name);
}

const SPEC = {
  properties: { title: "Q3 supplier review", author: "Operations", keywords: ["suppliers", "q3"] },
  page: { size: "A4", margins: 2 },
  header: "Q3 supplier review",
  footer: "Page {PAGE} of {PAGES}",
  blocks: [
    { title: "Q3 supplier review" },
    { subtitle: "Prepared 04 September" },
    { heading: "Summary", level: 1 },
    "Control-module ETA moved from 21 Aug to 29 Aug. It is now eight days late.",
    { paragraph: [{ text: "Owner: ", bold: true }, "Priya Shah – “ops” €12 ", { text: "policy", link: "https://openneko.app/policy" }] },
    { bullets: ["Call the supplier", { text: "Update the plan", items: ["Line 2", "Line 4"] }] },
    { numbered: ["First", "Second"] },
    { table: [["Supplier", "Days late", "Value"], ["Acme", 8, "$18,400"], ["Globex", 0, "$2,100"]], widths: [8, 4, 5] },
    { image: "IMAGE", width: 4, caption: "Figure 1" },
    { heading: "Detail", level: 2 },
    { markdown: "Ask **Acme** for a firm date by *Friday*.\n\n| Line | Units |\n|---|--:|\n| L2 | 49,310 |\n\n- one\n- two" },
    { pageBreak: true },
    { heading: "Appendix", level: 1 },
    { code: "SELECT sku FROM stock" },
  ],
};

test("create writes a document that read gives back", async () => {
  fs.writeFileSync(file("chart.png"), png(40, 20));
  const spec = { ...SPEC, blocks: SPEC.blocks.map((b) => (b.image ? { ...b, image: file("chart.png") } : b)) };
  const result = json("create", writeJson("spec.json", spec), file("created.pdf"));
  assert.equal(result.pages, 2);

  const md = cli("read", file("created.pdf"));
  for (const text of ["## Page 1", "Q3 supplier review", "eight days late", "Owner: Priya Shah – “ops” €12 policy", "• Call the supplier", "– Line 2", "1. First", "Supplier Days late Value", "Acme 8 $18,400", "Figure 1", "Ask Acme for a firm date by Friday.", "L2 49,310", "Page 1 of 2", "## Page 2", "Appendix", "SELECT sku FROM stock", "Page 2 of 2"]) {
    assert.ok(md.includes(text), `missing "${text}" in:\n${md}`);
  }
  const layout = cli("read", file("created.pdf"), "--layout", "--pages", "1");
  assert.match(layout, /Acme\s{3,}8\s+\$18,400/);

  const info = json("info", file("created.pdf"));
  assert.equal(info.title, "Q3 supplier review");
  assert.equal(info.author, "Operations");
  assert.equal(info.keywords, "suppliers q3");
  assert.deepEqual(info.pageSizes, { A4: "1-2" });
  assert.deepEqual(info.bookmarks, ["Summary", "Appendix"]);
  assert.equal(info.encrypted, false);
  assert.equal(info.form, null);
  assert.equal(info.pagesWithoutText, undefined);

  const doc = await load("created.pdf");
  const annots = doc.getPage(0).node.Annots();
  const uris = [];
  for (let i = 0; i < annots.size(); i++) uris.push(doc.context.lookup(annots.get(i)).lookup(PDFName.of("A")).lookup(PDFName.of("URI")).decodeText());
  assert.deepEqual(uris, ["https://openneko.app/policy"]);
  assert.equal(doc.catalog.lookup(PDFName.of("Lang")).decodeText(), "en");
  const json1 = JSON.parse(cli("read", file("created.pdf"), "--format", "json", "--pages", "2"));
  assert.equal(json1.pages.length, 1);
  assert.equal(json1.pages[0].page, 2);
});

test("long tables carry on to the next page with the header row", () => {
  const rows = [["Order", "Status"], ...Array.from({ length: 80 }, (_, i) => [`ORD-${1000 + i}`, i % 2 ? "Shipped" : "Held"])];
  json("create", writeJson("long.json", { blocks: [{ table: rows }] }), file("long.pdf"));
  const pages = JSON.parse(cli("read", file("long.pdf"), "--format", "json")).pages;
  assert.ok(pages.length >= 2);
  for (const page of pages) assert.ok(page.text.startsWith("Order Status"), `page ${page.page} has no header: ${page.text.slice(0, 40)}`);
  assert.ok(pages.at(-1).text.includes("ORD-1079"));
});

test("markdown and inline runs parse", () => {
  assert.deepEqual(parseInline("a **b** *c* `d` [e](https://x.y)"), [
    { text: "a " },
    { text: "b", bold: true },
    { text: " " },
    { text: "c", italic: true },
    { text: " " },
    { text: "d", code: true },
    { text: " " },
    { text: "e", link: "https://x.y" },
  ]);
  const blocks = markdownBlocks("# H\n\ntext\nmore\n\n- a\n  - b\n1. c\n\n---\n> q");
  assert.deepEqual(blocks.map((b) => Object.keys(b)[0]), ["heading", "paragraph", "bullets", "numbered", "rule", "quote"]);
  assert.deepEqual(blocks[2].bullets, [{ text: [{ text: "a" }], bullets: [[{ text: "b" }]] }]);
});

test("page ranges", () => {
  assert.deepEqual(parseRanges("2-4,7", 9), [2, 3, 4, 7]);
  assert.deepEqual(parseRanges("8-", 9), [8, 9]);
  assert.deepEqual(parseRanges("last", 9), [9]);
  assert.deepEqual(parseRanges(undefined, 3), [1, 2, 3]);
  assert.throws(() => parseRanges("4-12", 9), /outside 1-9/);
  assert.throws(() => parseRanges("x", 9), /not a page range/);
});

test("read pages through a long file and names the next pages", async () => {
  await textPdf("many.pdf", Array.from({ length: 12 }, (_, i) => `Page ${i + 1} ${"lorem ipsum ".repeat(30)}`));
  const out = cli("read", file("many.pdf"), "--max-chars", "300");
  assert.match(out, /Run read again with --pages (\d+)-12 for the rest/);
  const next = /--pages (\d+-12)/.exec(out)[1];
  assert.ok(cli("read", file("many.pdf"), "--pages", next).includes(`Page 12`));
});

test("fields lists a form and fill writes values that read back", async () => {
  await formPdf("form.pdf");
  const listed = json("fields", file("form.pdf"));
  assert.equal(listed.xfa, false);
  const byName = Object.fromEntries(listed.fields.map((f) => [f.name, f]));
  assert.deepEqual(Object.keys(byName).sort(), ["approved", "code", "locked", "notes", "region", "supplier.name", "tags", "tier"]);
  assert.equal(byName.code.maxLength, 4);
  assert.deepEqual(byName.tier.options, ["gold", "silver"]);
  assert.equal(byName.locked.readOnly, true);
  assert.equal(byName.notes.widgets[0].page, 2);

  const values = { "supplier.name": "Acme Ltd", code: 1234, approved: true, tier: "silver", region: "APAC", tags: ["a", "c"], notes: "Call on 29 Aug" };
  const result = json("fill", file("form.pdf"), writeJson("values.json", values), file("filled.pdf"));
  assert.deepEqual(result.notes, ["filled 7 fields"]);
  const after = Object.fromEntries(json("fields", file("filled.pdf")).fields.map((f) => [f.name, f.value]));
  assert.deepEqual(after, { "supplier.name": "Acme Ltd", code: "1234", approved: true, tier: "silver", region: "APAC", tags: ["a", "c"], locked: "fixed", notes: "Call on 29 Aug" });

  json("fill", file("filled.pdf"), writeJson("none.json", {}), file("flat.pdf"), "--flatten");
  assert.deepEqual(json("fields", file("flat.pdf")).fields, []);
  const text = cli("read", file("flat.pdf"));
  for (const value of ["Acme Ltd", "1234", "APAC", "Call on 29 Aug"]) assert.ok(text.includes(value), `flattened text lacks ${value}:\n${text}`);
});

test("fill refuses values the form does not allow", async () => {
  await formPdf("form2.pdf");
  const tryFill = (values) => cliFails("fill", file("form2.pdf"), writeJson("bad.json", values), file("bad.pdf"));
  assert.match(tryFill({ "supplier.nam": "x" }), /no field named "supplier.nam"; close names: supplier.name/);
  assert.match(tryFill({ code: "TOOLONG" }), /at most 4 characters/);
  assert.match(tryFill({ tier: "bronze" }), /no option "bronze"; options: gold, silver/);
  assert.match(tryFill({ region: ["EMEA", "APAC"] }), /takes one value/);
  assert.match(tryFill({ approved: "yes" }), /takes true or false/);
  assert.match(tryFill({ locked: "new" }), /read-only/);
  assert.match(tryFill({ "supplier.name": "नमस्ते" }), /cannot draw|font/);
  assert.equal(fs.existsSync(file("bad.pdf")), false);
  json("fill", file("form2.pdf"), writeJson("force.json", { values: { locked: "new" }, force: true }), file("forced.pdf"));
  assert.equal(json("fields", file("forced.pdf")).fields.find((f) => f.name === "locked").value, "new");
});

test("merge joins files and page ranges and keeps every form field", async () => {
  await formPdf("formA.pdf");
  await textPdf("letters.pdf", ["Alpha", "Bravo", "Charlie"]);
  const result = json("merge", file("merged.pdf"), file("formA.pdf"), `${file("letters.pdf")}:3,1`, file("formA.pdf"), "--bookmarks");
  assert.equal(result.pages, 6);
  assert.deepEqual(result.renamedFields.map((r) => r.to), ["supplier_2", "code_2", "approved_2", "tier_2", "region_2", "tags_2", "locked_2", "notes_2"]);
  const pages = JSON.parse(cli("read", file("merged.pdf"), "--format", "json")).pages.map((p) => p.text.split("\n")[0]);
  assert.deepEqual(pages, ["Supplier form", "Page two", "Charlie", "Alpha", "Supplier form", "Page two"]);
  const names = json("fields", file("merged.pdf")).fields.map((f) => f.name);
  assert.equal(names.length, 16);
  assert.ok(names.includes("supplier_2.name"));
  json("fill", file("merged.pdf"), writeJson("m.json", { "supplier.name": "First", "supplier_2.name": "Second" }), file("merged-filled.pdf"));
  const filled = Object.fromEntries(json("fields", file("merged-filled.pdf")).fields.map((f) => [f.name, f.value]));
  assert.equal(filled["supplier.name"], "First");
  assert.equal(filled["supplier_2.name"], "Second");
  assert.deepEqual(json("info", file("merged.pdf")).bookmarks, ["formA", "letters", "formA"]);
});

test("split writes one file per page or per range", async () => {
  await textPdf("five.pdf", ["One", "Two", "Three", "Four", "Five"]);
  const each = json("split", file("five.pdf"), file("each"));
  assert.equal(each.files.length, 5);
  assert.match(cli("read", each.files[3].file), /Four/);
  const ranges = json("split", file("five.pdf"), file("ranges"), "--ranges", "1-2 3-5");
  assert.deepEqual(ranges.files.map((f) => f.pages), ["1-2", "3-5"]);
  assert.equal((await PDFDocument.load(fs.readFileSync(ranges.files[1].file))).getPageCount(), 3);
  await formPdf("form3.pdf");
  const split = json("split", file("form3.pdf"), file("formsplit"));
  assert.deepEqual(json("fields", split.files[1].file).fields.map((f) => f.name), ["notes"]);
});

test("edit changes pages and draws on them", async () => {
  await textPdf("base.pdf", ["One", "Two", "Three", "Four"]);
  await textPdf("extra.pdf", ["Extra A", "Extra B"]);
  fs.writeFileSync(file("logo.png"), png(30, 10));
  const ops = [
    { op: "rotate", pages: "2", degrees: 90 },
    { op: "delete", pages: "3" },
    { op: "insert", file: file("extra.pdf"), pages: "2", after: 1 },
    { op: "move", pages: "4", to: "start" },
    { op: "insert", blank: true, at: "start" },
    { op: "reorder", order: "2-5,1" },
    { op: "text", pages: "1", text: "Reviewed 04 September", x: 2, y: 2, size: 10, color: "#C00000" },
    { op: "watermark", text: "DRAFT", pages: "all" },
    { op: "image", pages: "1", image: file("logo.png"), position: "top-right", width: 3 },
    { op: "pageNumbers", format: "Page {PAGE} of {PAGES}", pages: "1-4" },
    { op: "crop", pages: "5", top: 1, bottom: 1 },
    { op: "properties", title: "Edited", author: "OpenNeko" },
  ];
  const result = json("edit", file("base.pdf"), writeJson("ops.json", ops), file("edited.pdf"));
  assert.equal(result.pages, 5);
  const pages = JSON.parse(cli("read", file("edited.pdf"), "--format", "json")).pages;
  assert.match(pages[0].text, /Four/);
  assert.match(pages[0].text, /Reviewed 04 September/);
  assert.match(pages[1].text, /One/);
  assert.match(pages[2].text, /Extra B/);
  assert.match(pages[3].text, /Two/);
  for (const page of pages.slice(0, 4)) assert.match(page.text, /DRAFT/);
  assert.match(pages[3].text, /Page 4 of 4/);
  assert.equal(pages[4].text.replace(/DRAFT/, "").trim(), "");
  const doc = await load("edited.pdf");
  assert.equal(doc.getPage(3).getRotation().angle, 90);
  assert.equal(doc.getPage(0).getRotation().angle, 0);
  assert.equal(Math.round(doc.getPage(4).getCropBox().height), Math.round(842 - 2 * (72 / 2.54)));
  assert.equal(doc.getTitle(), "Edited");
  assert.equal(json("info", file("edited.pdf")).rotated[0].page, 4);
});

test("edit keeps form fields consistent when pages go", async () => {
  await formPdf("form4.pdf");
  json("edit", file("form4.pdf"), writeJson("del.json", [{ op: "delete", pages: "1" }]), file("form4-p2.pdf"));
  assert.deepEqual(json("fields", file("form4-p2.pdf")).fields.map((f) => f.name), ["notes"]);
  json("edit", file("form4.pdf"), writeJson("fillop.json", [{ op: "fill", values: { notes: "kept" } }, { op: "flatten" }]), file("form4-flat.pdf"));
  assert.match(cli("read", file("form4-flat.pdf"), "--pages", "2"), /kept/);
});

test("edit refuses what it cannot do safely", async () => {
  await textPdf("plain.pdf", ["Only page"]);
  const tryOps = (ops) => cliFails("edit", file("plain.pdf"), writeJson("bad-ops.json", ops), file("bad-out.pdf"));
  assert.match(tryOps([{ op: "redact", pages: "1" }]), /cannot redact/);
  assert.match(tryOps([{ op: "delete", pages: "1" }]), /every page/);
  assert.match(tryOps([{ op: "reorder", order: "1,1" }]), /each of the 1 pages once/);
  assert.match(tryOps([{ op: "spin" }]), /unknown op "spin"/);
  assert.match(tryOps([{ op: "rotate", pages: "1" }, { op: "text", pages: "1", text: "x" }]), /op 2 \(text\): .*needs x and y/);
  assert.match(tryOps([{ op: "text", pages: "1", text: "नमस्ते", x: 1, y: 1 }]), /cannot draw "न"/);
  assert.match(tryOps([{ op: "rotate", pages: "2" }]), /outside 1-1/);
  assert.equal(fs.existsSync(file("bad-out.pdf")), false);
  assert.match(cliFails("read", file("spec.json")), /not a PDF/);
  assert.match(cliFails("read", file("nope.pdf")), /no such file/);
  assert.match(cliFails("create", writeJson("badspec.json", { blocks: [{ heading: "x", level: 7 }] }), file("x.pdf")), /level must be 1 to 4/);
  assert.match(cliFails("create", writeJson("badspec2.json", { blocks: [{ widget: 1 }] }), file("x.pdf")), /unknown block/);
});

test("a custom font draws text that the built-in fonts cannot", (t) => {
  const font = ["/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf"].find((f) => fs.existsSync(f));
  if (!font) return t.skip("no TrueType font on this machine");
  json("create", writeJson("uni.json", { font: { name: font }, blocks: ["Margin ₹ 1,200 → Āž"] }), file("uni.pdf"));
  assert.match(cli("read", file("uni.pdf")), /Margin ₹ 1,200 → Āž/);
});

const IMAGE_FONTS = ["/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/usr/share/fonts-droid-fallback/truetype/DroidSansFallback.ttf"];

async function fontNames(pdfFile) {
  const doc = await PDFDocument.load(fs.readFileSync(pdfFile));
  return doc.context
    .enumerateIndirectObjects()
    .map(([, obj]) => obj.get?.(PDFName.of("Type")) === PDFName.of("Font") && obj.get(PDFName.of("BaseFont")))
    .filter(Boolean)
    .map((name) => name.decodeText().replace(/-\d+$/, ""));
}

test("the sandbox image's fonts draw the rupee sign, arrows, Greek, Cyrillic and CJK", async (t) => {
  if (!IMAGE_FONTS.every((f) => fs.existsSync(f))) return t.skip("the sandbox image's fallback fonts are not on this machine");
  const samples = ["Margin ₹ 1,200", "Up ↑ down ↓ next →", "Αθήνα λόγος", "Москва отчёт", "東京 日本語", "中文 报告", "한국어 보고서"];
  json("create", writeJson("fallback.json", { blocks: [{ heading: "Отчёт ₹" }, ...samples, { paragraph: [{ text: "Bold 東京 ₹", bold: true }] }, { table: [["Город", "金额"], ["Пуна", "₹ 4,200"]] }, { code: "total → ₹ 中" }] }), file("fallback.pdf"));
  const text = cli("read", file("fallback.pdf"));
  for (const sample of [...samples, "Отчёт ₹", "Bold 東京 ₹", "Город", "金额", "₹ 4,200", "total → ₹ 中"]) assert.ok(text.includes(sample), `the PDF lacks "${sample}":\n${text}`);
  const names = await fontNames(file("fallback.pdf"));
  for (const name of ["Helvetica", "DejaVuSans", "DejaVuSans-Bold", "DejaVuSansMono", "DroidSansFallback"]) assert.ok(names.includes(name), `no ${name} in ${names}`);

  json("create", writeJson("latin.json", { blocks: ["Plain text only"] }), file("latin.pdf"));
  assert.deepEqual((await fontNames(file("latin.pdf"))).filter((n) => !/^(Helvetica|Courier)/.test(n)), []);

  await textPdf("fallback-edit-in.pdf", ["Page one"]);
  json("edit", file("fallback-edit-in.pdf"), writeJson("fallback-ops.json", [
    { op: "text", pages: "1", text: "Δ ₹ 東京", x: 2, y: 2 },
    { op: "watermark", pages: "1", text: "ЧЕРНОВИК 草稿" },
    { op: "pageNumbers", format: "{PAGE} → {PAGES}" },
  ]), file("fallback-edit.pdf"));
  const edited = cli("read", file("fallback-edit.pdf"));
  for (const sample of ["Δ ₹ 東京", "ЧЕРНОВИК", "草稿", "1 → 1"]) assert.ok(edited.includes(sample), `the edited PDF lacks "${sample}":\n${edited}`);

  await formPdf("fallback-form.pdf");
  json("fill", file("fallback-form.pdf"), writeJson("fallback-values.json", { "supplier.name": "株式会社" }), file("fallback-filled.pdf"), "--flatten");
  assert.ok(cli("read", file("fallback-filled.pdf")).includes("株式会社"));
});

test("read gives the text of a font with a predefined CJK CMap", async () => {
  const doc = await PDFDocument.create();
  const page = doc.addPage([400, 400]);
  const c = doc.context;
  const descriptor = c.register(c.obj({ Type: "FontDescriptor", FontName: "HeiseiMin-W3", Flags: 6, FontBBox: [0, -120, 1000, 880], ItalicAngle: 0, Ascent: 880, Descent: -120, CapHeight: 700, StemV: 80 }));
  const cid = c.register(c.obj({ Type: "Font", Subtype: "CIDFontType0", BaseFont: "HeiseiMin-W3", CIDSystemInfo: { Registry: PDFHexString.fromText("Adobe"), Ordering: PDFHexString.fromText("Japan1"), Supplement: 2 }, FontDescriptor: descriptor }));
  page.node.setFontDictionary(PDFName.of("F1"), c.register(c.obj({ Type: "Font", Subtype: "Type0", BaseFont: "HeiseiMin-W3", Encoding: "UniJIS-UCS2-H", DescendantFonts: [cid] })));
  page.node.set(PDFName.of("Contents"), c.register(c.stream("BT /F1 24 Tf 50 300 Td <65E5672C8A9E> Tj ET")));
  fs.writeFileSync(file("cjk.pdf"), await doc.save());
  assert.match(cli("read", file("cjk.pdf")), /\u65e5\u672c\u8a9e/);
});

test("edit adds blank pages and resizes pages", async () => {
  await textPdf("resize.pdf", ["Wide page text"]);
  json("edit", file("resize.pdf"), writeJson("resize.json", [
    { op: "insert", blank: true, after: 1, count: 2, size: "Letter" },
    { op: "resize", pages: "1", size: "A5" },
  ]), file("resized.pdf"));
  const doc = await load("resized.pdf");
  assert.equal(doc.getPageCount(), 3);
  assert.deepEqual(Object.values(doc.getPage(0).getSize()).map(Math.round), [420, 595]);
  assert.deepEqual(Object.values(doc.getPage(2).getSize()).map(Math.round), [612, 792]);
  assert.match(cli("read", file("resized.pdf"), "--pages", "1"), /Wide page text/);
});

test("a signed file is refused until the user agrees to break the signature", async () => {
  const doc = await PDFDocument.create();
  const page = doc.addPage();
  const sig = doc.context.register(doc.context.obj({
    FT: "Sig",
    T: PDFString.of("Signature1"),
    V: { Type: "Sig", Filter: "Adobe.PPKLite" },
    Type: "Annot",
    Subtype: "Widget",
    Rect: [0, 0, 0, 0],
    P: page.ref,
  }));
  page.node.addAnnot(sig);
  doc.catalog.set(PDFName.of("AcroForm"), doc.context.obj({ Fields: [sig], SigFlags: 3 }));
  fs.writeFileSync(file("signed.pdf"), await doc.save());
  assert.deepEqual(json("info", file("signed.pdf")).signatures, ["Signature1"]);
  const ops = writeJson("rot.json", [{ op: "rotate", pages: "1" }]);
  assert.match(cliFails("edit", file("signed.pdf"), ops, file("signed-out.pdf")), /digitally signed \(Signature1\).*--break-signatures/);
  assert.match(cliFails("merge", file("signed-merge.pdf"), file("signed.pdf")), /digitally signed/);
  json("edit", file("signed.pdf"), ops, file("signed-out.pdf"), "--break-signatures");
  assert.equal((await load("signed-out.pdf")).getPage(0).getRotation().angle, 90);
});

test("an encrypted file can be read with its password and is not changed", async (t) => {
  const plain = await textPdf("secret-plain.pdf", ["Margin 75.5%"]);
  if (!python("pypdf")) {
    const doc = await PDFDocument.load(fs.readFileSync(plain));
    fs.writeFileSync(file("fake-enc.pdf"), await doc.save({ useObjectStreams: false }));
    const raw = fs.readFileSync(file("fake-enc.pdf"), "latin1").replace("\nxref\n", "\n99 0 obj\n<< /Filter /Standard /V 1 /R 2 >>\nendobj\nxref\n").replace("trailer\n<<", "trailer\n<<\n/Encrypt 99 0 R");
    fs.writeFileSync(file("fake-enc.pdf"), raw, "latin1");
    assert.match(cliFails("edit", file("fake-enc.pdf"), writeJson("r.json", [{ op: "rotate" }]), file("enc-out.pdf")), /is encrypted/);
    return t.skip("pypdf is not installed here; checked the refusal on a marked file only");
  }
  execFileSync("python3", ["-c", "import sys\nfrom pypdf import PdfReader, PdfWriter\nw = PdfWriter(clone_from=PdfReader(sys.argv[1]))\nw.encrypt('s3cret', algorithm='RC4-128')\nw.write(sys.argv[2])", plain, file("secret.pdf")]);
  assert.match(cliFails("read", file("secret.pdf")), /needs a password; add --password/);
  assert.match(cliFails("read", file("secret.pdf"), "--password", "nope"), /password .* is wrong/);
  assert.match(cli("read", file("secret.pdf"), "--password", "s3cret"), /Margin 75.5%/);
  assert.equal(json("info", file("secret.pdf"), "--password", "s3cret").encrypted, true);
  assert.match(cliFails("edit", file("secret.pdf"), writeJson("r2.json", [{ op: "rotate" }]), file("enc-out.pdf")), /is encrypted/);
});

test("ocr reads a scanned page", async (t) => {
  if (!has("pdftoppm") || !has("tesseract")) return t.skip("pdftoppm or tesseract is not installed here");
  const source = await PDFDocument.create();
  const font = await source.embedFont(StandardFonts.HelveticaBold);
  source.addPage([595, 842]).drawText("Invoice 4471 total 18400", { x: 60, y: 700, size: 28, font });
  fs.writeFileSync(file("to-scan.pdf"), await source.save());
  const rendered = json("render", file("to-scan.pdf"), file("scan"), "--pages", "1", "--dpi", "150");
  assert.equal(rendered.files.length, 1);
  const scan = await PDFDocument.create();
  const image = await scan.embedPng(fs.readFileSync(rendered.files[0]));
  scan.addPage([595, 842]).drawImage(image, { x: 0, y: 0, width: 595, height: 842 });
  fs.writeFileSync(file("scanned.pdf"), await scan.save());
  assert.equal(json("info", file("scanned.pdf")).pagesWithoutText, "1");
  assert.match(cli("read", file("scanned.pdf")), /no text layer; if it is a scan, run ocr --pages 1/);
  assert.match(cli("ocr", file("scanned.pdf")), /Invoice 4471 total 18400/);
});

test("files from create, edit, fill and merge open in pypdf", (t) => {
  if (!python("pypdf")) return t.skip("pypdf is not installed here");
  const script = "import sys\nfrom pypdf import PdfReader\nfor f in sys.argv[1:]:\n    r = PdfReader(f, strict=True)\n    print(f, len(r.pages), len(''.join(p.extract_text() for p in r.pages)) > 0, sorted((r.get_fields() or {}).keys()))";
  const out = execFileSync("python3", ["-c", script, file("created.pdf"), file("edited.pdf"), file("filled.pdf"), file("merged.pdf")], { encoding: "utf8" });
  const lines = out.trim().split("\n");
  assert.equal(lines.length, 4);
  for (const line of lines) assert.match(line, / True /);
  assert.match(lines[3], /'supplier_2.name'/);
});
