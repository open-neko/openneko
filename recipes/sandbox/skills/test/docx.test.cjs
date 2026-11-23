"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const zlib = require("node:zlib");
const { execFileSync, spawnSync } = require("node:child_process");
const { zipSync, unzipSync, strToU8, strFromU8 } = require("fflate");
const { XMLValidator } = require("fast-xml-parser");
const { markdownBlocks, parseInline } = require("../docx/scripts/lib/build.cjs");

const CLI = path.join(__dirname, "..", "docx", "scripts", "docx.cjs");
const dir = fs.mkdtempSync(path.join(os.tmpdir(), "docx-skill-"));
test.after(() => fs.rmSync(dir, { recursive: true, force: true }));

function cli(...args) {
  return execFileSync(process.execPath, [CLI, ...args], { encoding: "utf8" });
}

function cliFails(...args) {
  const result = spawnSync(process.execPath, [CLI, ...args], { encoding: "utf8" });
  assert.notEqual(result.status, 0, `expected a failure, got: ${result.stdout}`);
  return result.stderr;
}

function writeJson(name, value) {
  const file = path.join(dir, name);
  fs.writeFileSync(file, JSON.stringify(value));
  return file;
}

function parts(file) {
  return Object.fromEntries(Object.entries(unzipSync(new Uint8Array(fs.readFileSync(file)))).map(([name, bytes]) => [name, name.endsWith(".xml") || name.endsWith(".rels") ? strFromU8(bytes) : bytes]));
}

function assertWellFormed(file) {
  for (const [name, body] of Object.entries(parts(file))) {
    if (typeof body !== "string") continue;
    const result = XMLValidator.validate(body);
    assert.equal(result, true, `${name} is not well-formed: ${JSON.stringify(result)}`);
  }
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
  for (let r = 0; r < height; r++) rows[r * (width * 3 + 1)] = 0;
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk("IHDR", header), chunk("IDAT", zlib.deflateSync(rows)), chunk("IEND", Buffer.alloc(0))]);
}

const HEAD = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n';
const W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"';
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";

function foreignDocument(file) {
  const body = [
    '<w:p><w:pPr><w:pStyle w:val="Kop1"/></w:pPr><w:r><w:t>Terms</w:t></w:r></w:p>',
    '<w:p><w:r><w:t xml:space="preserve">The supplier’s </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>deliv</w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">ery date is </w:t></w:r><w:r><w:t>21 Aug</w:t></w:r><w:r><w:t>.</w:t></w:r><w:r><w:footnoteReference w:id="1"/></w:r></w:p>',
    '<w:p><w:r><w:t xml:space="preserve">Page </w:t></w:r><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>3</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>',
    '<w:p><w:r><w:t xml:space="preserve">Payment in </w:t></w:r><w:ins w:id="90" w:author="Legal" w:date="2026-10-01T00:00:00Z"><w:r><w:t>thirty</w:t></w:r></w:ins><w:r><w:t xml:space="preserve"> days.</w:t></w:r></w:p>',
    '<w:p><w:hyperlink r:id="rId9"><w:r><w:t>Portal</w:t></w:r></w:hyperlink><w:r><w:t xml:space="preserve"> link.</w:t></w:r></w:p>',
    '<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/></w:tblPr><w:tblGrid><w:gridCol w:w="4000"/><w:gridCol w:w="4000"/></w:tblGrid><w:tr><w:tc><w:p><w:r><w:t>Item</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Price</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>Bolt</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:rPr><w:i/></w:rPr><w:t>4.10</w:t></w:r></w:p></w:tc></w:tr></w:tbl>',
    '<w:p><w:r><w:t>Closing note.</w:t></w:r></w:p>',
    '<w:sectPr><w:pgSz w:w="12240" w:h="15840"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr>',
  ].join("");
  const files = {
    "[Content_Types].xml": `${HEAD}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/footnotes.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footnotes+xml"/><Override PartName="/docProps/custom.xml" ContentType="application/vnd.openxmlformats-officedocument.custom-properties+xml"/></Types>`,
    "_rels/.rels": `${HEAD}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="${REL}/custom-properties" Target="docProps/custom.xml"/></Relationships>`,
    "docProps/custom.xml": `${HEAD}<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties"><property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="2" name="Matter"><vt:lpwstr xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">M-104</vt:lpwstr></property></Properties>`,
    "word/_rels/document.xml.rels": `${HEAD}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/styles" Target="styles.xml"/><Relationship Id="rId2" Type="${REL}/footnotes" Target="footnotes.xml"/><Relationship Id="rId9" Type="${REL}/hyperlink" Target="https://portal.example.com" TargetMode="External"/></Relationships>`,
    "word/styles.xml": `${HEAD}<w:styles ${W}><w:style w:type="paragraph" w:default="1" w:styleId="Standaard"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="Kop1"><w:name w:val="heading 1"/><w:basedOn w:val="Standaard"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`,
    "word/footnotes.xml": `${HEAD}<w:footnotes ${W}><w:footnote w:id="1"><w:p><w:r><w:t>Per the master agreement.</w:t></w:r></w:p></w:footnote></w:footnotes>`,
    "word/document.xml": `${HEAD}<w:document ${W}><w:body>${body}</w:body></w:document>`,
  };
  fs.writeFileSync(file, zipSync(Object.fromEntries(Object.entries(files).map(([name, text]) => [name, strToU8(text)]))));
  return file;
}

test("the docx and xlsx skills carry the same OOXML layer", () => {
  const docx = fs.readFileSync(path.join(__dirname, "..", "docx", "scripts", "lib", "ooxml.cjs"), "utf8");
  const xlsx = fs.readFileSync(path.join(__dirname, "..", "xlsx", "scripts", "lib", "ooxml.cjs"), "utf8");
  assert.equal(docx, xlsx);
});

test("markdown turns into blocks with inline formatting", () => {
  assert.deepEqual(parseInline("a **b** *c* `d` [e](https://x.test) \\*f"), [
    { text: "a " },
    { text: "b", bold: true },
    { text: " " },
    { text: "c", italic: true },
    { text: " " },
    { text: "d", code: true },
    { text: " " },
    { text: "e", link: "https://x.test" },
    { text: " *f" },
  ]);
  assert.deepEqual(parseInline("snake_case_name stays"), [{ text: "snake_case_name stays" }]);
  const blocks = markdownBlocks("# Title\n\nOne\ntwo\n\n- a\n  - b\n- c\n\n| H | I |\n|---|---|\n| 1 | 2 |\n\n```\ncode line\n```");
  assert.deepEqual(blocks.map((block) => Object.keys(block)[0]), ["heading", "paragraph", "bullets", "table", "paragraph"]);
  assert.deepEqual(blocks[1].paragraph, [{ text: "One two" }]);
  assert.equal(blocks[2].bullets[0].items[0].text[0].text, "b");
  assert.equal(blocks[2].bullets.length, 2);
  assert.equal(blocks[3].table.length, 2);
});

test("create writes a document that reads back with its structure", () => {
  const image = path.join(dir, "chart.png");
  fs.writeFileSync(image, png(40, 20));
  const spec = writeJson("spec.json", {
    properties: { title: "Q3 supplier review", author: "Ops" },
    page: { size: "Letter", orientation: "landscape", margins: 2 },
    header: "Supplier review",
    footer: "Page {PAGE} of {PAGES}",
    font: { name: "Arial", size: 10.5 },
    blocks: [
      { title: "Q3 supplier review" },
      { heading: "Summary", level: 1 },
      "Control-module ETA moved from 21 Aug to 29 Aug.",
      { paragraph: [{ text: "Owner: ", bold: true }, { text: "portal", link: "https://openneko.app" }], align: "justify" },
      { bullets: ["First", { text: "Second", items: ["Inner"] }] },
      { numbered: ["Call", "Plan"] },
      { table: [["Supplier", "Days"], ["Acme", 8]], widths: [10, 5], caption: "Table 1. Late suppliers" },
      { image, width: 4, caption: "Figure 1" },
      { pageBreak: true },
      { toc: true },
    ],
  });
  const out = path.join(dir, "created.docx");
  const summary = JSON.parse(cli("create", spec, out));
  assert.equal(summary.file, out);
  assertWellFormed(out);
  const files = parts(out);
  assert.ok(files["word/numbering.xml"].includes("OpenNeko bullets"));
  assert.ok(files["word/header1.xml"].includes("Supplier review"));
  assert.ok(files["word/footer1.xml"].includes("NUMPAGES"));
  assert.ok(files["word/document.xml"].includes('w:orient="landscape"'));
  assert.ok(files["word/document.xml"].includes('<wp:extent cx="1440000" cy="720000"/>'));
  assert.ok(files["word/media/openneko-image1.png"] instanceof Uint8Array);
  assert.ok(files["[Content_Types].xml"].includes('Extension="png"'));
  assert.ok(files["word/settings.xml"].includes("w:updateFields"));
  assert.ok(files["word/styles.xml"].includes('w:ascii="Arial"') && files["word/styles.xml"].includes('<w:sz w:val="21"/>'));
  assert.ok(files["docProps/core.xml"].includes("<dc:title>Q3 supplier review</dc:title>"));

  const text = cli("read", out);
  assert.match(text, /^# Q3 supplier review/m);
  assert.match(text, /^# Summary/m);
  assert.match(text, /\*\*Owner:\*\* \[portal\]\(https:\/\/openneko\.app\)/);
  assert.match(text, /^- First\n- Second\n {2}- Inner\n1\. Call\n1\. Plan/m);
  assert.match(text, /\| \*\*Supplier\*\* \| \*\*Days\*\* \|\n\|---\|---\|\n\| Acme \| 8 \|/);
  assert.match(text, /!\[Figure 1\]/);
  const json = JSON.parse(cli("read", out, "--format", "json"));
  assert.equal(json.blocks.find((block) => block.type === "table").rows[1][0], "Acme");
  const info = JSON.parse(cli("info", out));
  assert.equal(info.properties.title, "Q3 supplier review");
  assert.deepEqual(info.sections, [{ page: "Letter", orientation: "landscape", headers: 1, footers: 1 }]);
  assert.equal(info.images, 1);
  assert.equal(info.tables[0].rows, 2);
});

test("edit finds text across runs and curly quotes, and keeps the parts it does not change", () => {
  const input = foreignDocument(path.join(dir, "foreign.docx"));
  const out = path.join(dir, "foreign-edited.docx");
  const ops = writeJson("ops.json", [
    { op: "replace", find: "supplier's delivery date is 21 Aug", with: "supplier's delivery date is 29 Aug" },
    { op: "insert", after: { heading: "Terms" }, blocks: [{ heading: "Scope", level: 2 }, "New scope text."] },
    { op: "cell", table: 1, row: 2, column: 2, text: "4.25" },
    { op: "addRow", table: 1, cells: ["Nut", "0.40"] },
    { op: "delete", find: "Closing note." },
    { op: "properties", title: "Terms v2" },
  ]);
  cli("edit", input, ops, out);
  assertWellFormed(out);
  const before = parts(input);
  const after = parts(out);
  assert.equal(after["docProps/custom.xml"], before["docProps/custom.xml"]);
  assert.equal(after["word/footnotes.xml"], before["word/footnotes.xml"]);
  assert.ok(after["word/document.xml"].includes('<w:footnoteReference w:id="1"/>'));
  assert.ok(after["word/document.xml"].includes("<w:instrText xml:space=\"preserve\"> PAGE </w:instrText>"));
  assert.ok(after["word/document.xml"].includes('w:author="Legal"'));
  assert.ok(after["word/styles.xml"].includes('w:styleId="Kop1"'));
  assert.ok(after["word/styles.xml"].includes('w:styleId="Heading2"'));

  const text = cli("read", out, "--numbers");
  assert.match(text, /\[1\] # Terms\n\n\[2\] ## Scope\n\n\[3\] New scope text\./);
  assert.match(text, /\[4\] The supplier's delivery date is 29 Aug\.\[\^1\]/);
  assert.match(text, /Page 3/);
  assert.match(text, /Payment in \{\+\+thirty\+\+\} days\./);
  assert.match(text, /\[Portal\]\(https:\/\/portal\.example\.com\) link\./);
  assert.match(text, /\| \[\d+\] Bolt \| \[\d+\] \*4\.25\* \|/);
  assert.match(text, /\| \[\d+\] Nut \| \[\d+\] \*0\.40\* \|/);
  assert.doesNotMatch(text, /Closing note/);
  assert.ok(after["word/document.xml"].includes("<w:i/></w:rPr><w:t>4.25</w:t>"));
  assert.equal(JSON.parse(cli("info", out)).properties.title, "Terms v2");
});

test("tracked edits list as changes, and accept and reject resolve them", () => {
  const input = foreignDocument(path.join(dir, "tracked-in.docx"));
  const out = path.join(dir, "tracked.docx");
  const ops = writeJson("tracked-ops.json", {
    track: true,
    author: "Reviewer",
    ops: [
      { op: "replace", find: "21 Aug", with: "29 Aug" },
      { op: "insert", after: "Payment in", text: "Late fees apply." },
      { op: "delete", find: "Closing note." },
      { op: "format", find: "Portal", italic: true },
      { op: "cell", table: 1, row: 2, column: 2, text: "4.25" },
      { op: "addRow", table: 1, after: 1, cells: ["Nut", "0.40"] },
      { op: "deleteRow", table: 1, row: 3 },
      { op: "style", find: "Late fees", heading: 3 },
      { op: "comment", find: "29 Aug", text: "Supplier confirmed by email." },
      { op: "trackChanges" },
    ],
  });
  const result = JSON.parse(cli("edit", input, ops, out));
  assert.equal(result.tracked, true);
  assertWellFormed(out);
  const doc = parts(out)["word/document.xml"];
  assert.ok(doc.includes("<w:delText>21 Aug</w:delText>"));
  assert.ok(parts(out)["word/settings.xml"].includes("<w:trackRevisions/>"));

  const changes = JSON.parse(cli("changes", out));
  const types = changes.map((change) => change.type).sort();
  for (const type of ["delete", "insert", "paragraph inserted", "paragraph deleted", "format", "paragraph format", "row inserted", "row deleted"]) assert.ok(types.includes(type), `missing ${type}: ${types}`);
  assert.ok(changes.filter((change) => change.author === "Reviewer").length >= 8);
  assert.ok(changes.some((change) => change.author === "Legal" && change.text === "thirty"));
  const ids = new Set(changes.map((change) => change.id));
  assert.equal(ids.size, changes.length);

  const comments = JSON.parse(cli("comments", out));
  assert.deepEqual(comments.map((c) => [c.author, c.anchor, c.text]), [["Reviewer", "29 Aug", "Supplier confirmed by email."]]);

  const shown = cli("read", out);
  assert.match(shown, /\{--21 Aug--\}\{\+\+29 Aug\+\+\}/);
  assert.match(shown, /\{--Closing note\.--\}/);

  const accepted = path.join(dir, "accepted.docx");
  cli("edit", out, writeJson("accept-reviewer.json", [{ op: "accept", author: "Reviewer" }]), accepted);
  assertWellFormed(accepted);
  const acceptedText = cli("read", accepted);
  assert.match(acceptedText, /\*\*delivery date is\*\* 29 Aug\.\[\^1\]/);
  assert.match(acceptedText, /^### Late fees apply\.$/m);
  assert.doesNotMatch(acceptedText, /Closing note/);
  assert.match(acceptedText, /\{\+\+thirty\+\+\}/);
  assert.match(acceptedText, /\| Nut \| 0\.40 \|/);
  assert.doesNotMatch(acceptedText, /\| Bolt \|/);
  assert.deepEqual(JSON.parse(cli("changes", accepted)).map((c) => c.author), ["Legal"]);
  assert.doesNotMatch(parts(accepted)["word/document.xml"].replace(/<w:ins w:id="90"[^>]*>/, ""), /w:delText|<w:del |<w:ins |w:rPrChange|w:pPrChange/);

  const rejected = path.join(dir, "rejected.docx");
  cli("edit", out, writeJson("reject-all.json", [{ op: "reject" }]), rejected);
  assertWellFormed(rejected);
  const rejectedXml = parts(rejected)["word/document.xml"];
  assert.doesNotMatch(rejectedXml, /w:delText|<w:del |<w:ins |w:rPrChange|w:pPrChange/);
  assert.ok(rejectedXml.includes("<w:t>21 Aug</w:t>"));
  assert.ok(rejectedXml.includes("<w:t>Closing note.</w:t>"));
  const original = cli("read", input, "--changes", "reject");
  assert.equal(cli("read", rejected), original);
  assert.equal(cli("read", out, "--changes", "reject"), original);
});

test("edit refuses unclear or unsafe requests with a message", () => {
  const input = foreignDocument(path.join(dir, "refuse.docx"));
  const out = path.join(dir, "refused.docx");
  assert.match(cliFails("edit", input, writeJson("r2.json", [{ op: "replace", find: "a", with: "b" }]), out), /occurs \d+ times/);
  assert.match(cliFails("edit", input, writeJson("r3.json", [{ op: "replace", find: "no such words", with: "b" }]), out), /is not in the document body/);
  assert.match(cliFails("edit", input, writeJson("r4.json", [{ op: "replace", find: "3", with: "4" }]), out), /inside a field/);
  assert.match(cliFails("edit", input, writeJson("r5.json", { track: true, ops: [{ op: "replace", find: "thirty", with: "sixty" }] }), out), /another author's tracked insertion/);
  assert.match(cliFails("edit", input, writeJson("r6.json", [{ op: "insert", after: { paragraph: 99 }, text: "x" }]), out), /no paragraph 99/);
  assert.match(cliFails("edit", input, writeJson("r7.json", [{ op: "explode" }]), out), /op 1 \(explode\): unknown op/);
  assert.match(cliFails("edit", input, writeJson("r8.json", [{ op: "insert", at: "end", blocks: [{ heading: "H", level: 9 }] }]), out), /heading level must be 1 to 6/);
  assert.equal(fs.existsSync(out), false);
  fs.writeFileSync(path.join(dir, "old.doc"), "x");
  assert.match(cliFails("read", path.join(dir, "old.doc")), /\.doc files are not supported/);
});

test("a second edit on the reviewer's own insertion changes it in place", () => {
  const input = foreignDocument(path.join(dir, "own.docx"));
  const first = path.join(dir, "own-1.docx");
  const second = path.join(dir, "own-2.docx");
  cli("edit", input, writeJson("own-1.json", [{ op: "replace", find: "Closing note.", with: "Closing remarks." }]), first, "--track", "--author", "Reviewer");
  cli("edit", first, writeJson("own-2.json", [{ op: "replace", find: "remarks", with: "words" }]), second, "--track", "--author", "Reviewer");
  assertWellFormed(second);
  assert.match(cli("read", second), /\{--Closing note\.--\}\{\+\+Closing words\.\+\+\}/);
});

test("python-docx opens the created and edited files", (t) => {
  const probe = spawnSync("python3", ["-I", "-c", "import docx"], { encoding: "utf8" });
  if (probe.status !== 0) {
    t.skip("python-docx is not installed");
    return;
  }
  const created = path.join(dir, "created.docx");
  const edited = path.join(dir, "foreign-edited.docx");
  const script = "import sys, docx\nfor f in sys.argv[1:]:\n    d = docx.Document(f)\n    print(len(d.paragraphs), len(d.tables), d.paragraphs[0].text)\n";
  const result = spawnSync("python3", ["-I", "-c", script, created, edited], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  const [a, b] = result.stdout.trim().split("\n");
  assert.match(a, /^\d+ 1 Q3 supplier review$/);
  assert.match(b, /^\d+ 1 Terms$/);
});
