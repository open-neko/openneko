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
const T = require("../pptx/scripts/lib/template.cjs");

const CLI = path.join(__dirname, "..", "pptx", "scripts", "pptx.cjs");
const dir = fs.mkdtempSync(path.join(os.tmpdir(), "pptx-skill-"));
test.after(() => fs.rmSync(dir, { recursive: true, force: true }));

function cli(...args) {
  return execFileSync(process.execPath, [CLI, ...args], { encoding: "utf8" });
}

function json(...args) {
  return JSON.parse(cli(...args));
}

function cliFails(...args) {
  const result = spawnSync(process.execPath, [CLI, ...args], { encoding: "utf8" });
  assert.notEqual(result.status, 0, `expected a failure, got: ${result.stdout}`);
  return result.stderr;
}

function file(name) {
  return path.join(dir, name);
}

function writeJson(name, value) {
  fs.writeFileSync(file(name), JSON.stringify(value));
  return file(name);
}

function parts(target) {
  return Object.fromEntries(Object.entries(unzipSync(new Uint8Array(fs.readFileSync(target)))).map(([name, bytes]) => [name, name.endsWith(".xml") || name.endsWith(".rels") ? strFromU8(bytes) : bytes]));
}

function assertWellFormed(target) {
  for (const [name, body] of Object.entries(parts(target))) {
    if (typeof body !== "string") continue;
    const result = XMLValidator.validate(body);
    assert.equal(result, true, `${name} is not well-formed: ${JSON.stringify(result)}`);
  }
}

function assertRelsResolve(target) {
  const all = parts(target);
  for (const [name, body] of Object.entries(all)) {
    if (!name.endsWith(".rels")) continue;
    const base = name.replace(/_rels\/([^/]*)\.rels$/, "$1");
    for (const match of body.matchAll(/<Relationship [^>]*>/g)) {
      const tag = match[0];
      if (/TargetMode="External"/.test(tag)) continue;
      const targetPath = /Target="([^"]+)"/.exec(tag)[1];
      const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(base), targetPath)).replace(/^\//, "");
      assert.ok(resolved in all, `${name} points at missing ${resolved}`);
    }
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
const NS = 'xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"';
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const PML = "application/vnd.openxmlformats-officedocument.presentationml";
const GROUP = '<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>';
const CUSTOM_XML = `${HEAD}<root xmlns="urn:example">kept</root>`;

function rels(list) {
  return `${HEAD}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${list.map(([id, type, target, external]) => `<Relationship Id="${id}" Type="${type.startsWith("http") ? type : `${REL}/${type}`}" Target="${target}"${external ? ' TargetMode="External"' : ""}/>`).join("")}</Relationships>`;
}

function ph(id, name, attrs, box, text = "") {
  const xfrm = box ? `<a:xfrm><a:off x="${box[0]}" y="${box[1]}"/><a:ext cx="${box[2]}" cy="${box[3]}"/></a:xfrm>` : "";
  return `<p:sp><p:nvSpPr><p:cNvPr id="${id}" name="${name}"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph ${attrs}/></p:nvPr></p:nvSpPr><p:spPr>${xfrm}</p:spPr><p:txBody><a:bodyPr/><a:lstStyle/>${text || "<a:p><a:endParaRPr/></a:p>"}</p:txBody></p:sp>`;
}

function foreignDeck(target) {
  const master = `${HEAD}<p:sldMaster ${NS}><p:cSld><p:spTree>${GROUP}${ph(2, "Titelplatzhalter 1", 'type="title"', [457200, 274638, 8229600, 1143000])}${ph(3, "Textplatzhalter 2", 'type="body" idx="1"', [457200, 1600200, 8229600, 4525963])}</p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/><p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/><p:sldLayoutId id="2147483650" r:id="rId2"/></p:sldLayoutIdLst><p:txStyles><p:titleStyle><a:lvl1pPr><a:defRPr sz="4000"/></a:lvl1pPr></p:titleStyle><p:bodyStyle><a:lvl1pPr marL="342900" indent="-342900"><a:buChar char="•"/><a:defRPr sz="2800"/></a:lvl1pPr></p:bodyStyle><p:otherStyle><a:lvl1pPr><a:defRPr sz="1800"/></a:lvl1pPr></p:otherStyle></p:txStyles></p:sldMaster>`;
  const layout1 = `${HEAD}<p:sldLayout ${NS} type="obj" preserve="1"><p:cSld name="Titel und Inhalt"><p:spTree>${GROUP}${ph(2, "Titel 1", 'type="title"')}${ph(3, "Inhaltsplatzhalter 2", 'idx="1"')}</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`;
  const layout2 = `${HEAD}<p:sldLayout ${NS} preserve="1" userDrawn="1"><p:cSld name="Kundenfolie"><p:spTree>${GROUP}${ph(2, "Titel 1", 'type="title"')}${ph(3, "Kunde", 'type="body" sz="quarter" idx="13"', [457200, 1600200, 4000000, 800000])}${ph(4, "Logo", 'type="pic" sz="quarter" idx="14"', [5000000, 1600200, 3000000, 3000000])}</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`;
  const run = (text, extra = "") => `<a:r><a:rPr lang="de-DE"${extra}/><a:t>${text}</a:t></a:r>`;
  const slide1 = `${HEAD}<p:sld ${NS}><p:cSld><p:spTree>${GROUP}${ph(2, "Titel 1", 'type="title"', null, `<a:p>${run("The supplier’s ")}${run("deliv", ' b="1"')}${run("ery date is ", ' b="1"')}${run("21 Aug.")}</a:p>`)}<p:grpSp><p:nvGrpSpPr><p:cNvPr id="7" name="Gruppe 6"/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="457200" y="2000000"/><a:ext cx="4000000" cy="1000000"/><a:chOff x="457200" y="2000000"/><a:chExt cx="4000000" cy="1000000"/></a:xfrm></p:grpSpPr><p:sp><p:nvSpPr><p:cNvPr id="8" name="Textfeld 7"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="457200" y="2000000"/><a:ext cx="4000000" cy="500000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p>${run("Prepared for {{client}}")}<a:r><a:rPr lang="de-DE"><a:hlinkClick r:id="rId9"/></a:rPr><a:t> portal</a:t></a:r></a:p></p:txBody></p:sp></p:grpSp></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr><p:timing><p:tnLst><p:par><p:cTn id="1" dur="indefinite" restart="never" nodeType="tmRoot"/></p:par></p:tnLst><p:bldLst><p:bldP spid="8" grpId="0"/></p:bldLst></p:timing></p:sld>`;
  const table = '<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="4" name="Tabelle 3"/><p:cNvGraphicFramePr><a:graphicFrameLocks noGrp="1"/></p:cNvGraphicFramePr><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="457200" y="3500000"/><a:ext cx="6000000" cy="740000"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl><a:tblPr firstRow="1"/><a:tblGrid><a:gridCol w="3000000"/><a:gridCol w="3000000"/></a:tblGrid><a:tr h="370000"><a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="de-DE" b="1"/><a:t>Teil</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc><a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="de-DE" b="1"/><a:t>Preis</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc></a:tr><a:tr h="370000"><a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="de-DE" sz="1400"><a:solidFill><a:srgbClr val="C00000"/></a:solidFill></a:rPr><a:t>Bolzen</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc><a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="de-DE"/><a:t>4,10</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc></a:tr></a:tbl></a:graphicData></a:graphic></p:graphicFrame>';
  const slide2 = `${HEAD}<p:sld ${NS}><p:cSld><p:spTree>${GROUP}${ph(2, "Titel 1", 'type="title"', null, `<a:p>${run("Agenda")}</a:p>`)}${ph(3, "Inhalt 2", 'idx="1"', null, `<a:p>${run("Welcome {{client}}")}</a:p><a:p><a:pPr lvl="1"/>${run("Second level")}</a:p>`)}${table}</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`;
  const notesMaster = `${HEAD}<p:notesMaster ${NS}><p:cSld><p:spTree>${GROUP}${ph(2, "Notizen", 'type="body" idx="1"', [685800, 4400550, 5486400, 3600450])}</p:spTree></p:cSld><p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/></p:notesMaster>`;
  const notes = `${HEAD}<p:notes ${NS}><p:cSld><p:spTree>${GROUP}${ph(2, "Notizen 1", 'type="body" idx="1"', null, `<a:p>${run("Greet {{client}} first.")}</a:p>`)}</p:spTree></p:cSld></p:notes>`;
  const presentation = `${HEAD}<p:presentation ${NS}><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst><p:notesMasterIdLst><p:notesMasterId r:id="rId6"/></p:notesMasterIdLst><p:sldIdLst><p:sldId id="256" r:id="rId2"/><p:sldId id="257" r:id="rId3"/></p:sldIdLst><p:sldSz cx="9144000" cy="6858000" type="screen4x3"/><p:notesSz cx="6858000" cy="9144000"/><p:extLst><p:ext uri="{521415D9-36F7-43E2-AB2F-B90AF26B5E84}"><p14:sectionLst xmlns:p14="http://schemas.microsoft.com/office/powerpoint/2010/main"><p14:section name="Start" id="{11111111-1111-1111-1111-111111111111}"><p14:sldIdLst><p14:sldId id="256"/></p14:sldIdLst></p14:section><p14:section name="Rest" id="{22222222-2222-2222-2222-222222222222}"><p14:sldIdLst><p14:sldId id="257"/></p14:sldIdLst></p14:section></p14:sectionLst></p:ext></p:extLst></p:presentation>`;
  const files = {
    "[Content_Types].xml": `${HEAD}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="${PML}.presentation.main+xml"/><Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="${PML}.slideMaster+xml"/><Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="${PML}.slideLayout+xml"/><Override PartName="/ppt/slideLayouts/slideLayout2.xml" ContentType="${PML}.slideLayout+xml"/><Override PartName="/ppt/slides/slide1.xml" ContentType="${PML}.slide+xml"/><Override PartName="/ppt/slides/slide2.xml" ContentType="${PML}.slide+xml"/><Override PartName="/ppt/notesMasters/notesMaster1.xml" ContentType="${PML}.notesMaster+xml"/><Override PartName="/ppt/notesSlides/notesSlide1.xml" ContentType="${PML}.notesSlide+xml"/><Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/><Override PartName="/ppt/theme/theme2.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/><Override PartName="/docProps/custom.xml" ContentType="application/vnd.openxmlformats-officedocument.custom-properties+xml"/></Types>`,
    "_rels/.rels": rels([["rId1", "officeDocument", "ppt/presentation.xml"], ["rId2", "custom-properties", "docProps/custom.xml"]]),
    "docProps/custom.xml": `${HEAD}<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="2" name="Klasse"><vt:lpwstr>intern</vt:lpwstr></property></Properties>`,
    "customXml/item1.xml": CUSTOM_XML,
    "ppt/presentation.xml": presentation,
    "ppt/_rels/presentation.xml.rels": rels([["rId1", "slideMaster", "slideMasters/slideMaster1.xml"], ["rId2", "slide", "slides/slide1.xml"], ["rId3", "slide", "slides/slide2.xml"], ["rId5", "theme", "theme/theme1.xml"], ["rId6", "notesMaster", "notesMasters/notesMaster1.xml"]]),
    "ppt/slideMasters/slideMaster1.xml": master,
    "ppt/slideMasters/_rels/slideMaster1.xml.rels": rels([["rId1", "slideLayout", "../slideLayouts/slideLayout1.xml"], ["rId2", "slideLayout", "../slideLayouts/slideLayout2.xml"], ["rId3", "theme", "../theme/theme1.xml"]]),
    "ppt/slideLayouts/slideLayout1.xml": layout1,
    "ppt/slideLayouts/_rels/slideLayout1.xml.rels": rels([["rId1", "slideMaster", "../slideMasters/slideMaster1.xml"]]),
    "ppt/slideLayouts/slideLayout2.xml": layout2,
    "ppt/slideLayouts/_rels/slideLayout2.xml.rels": rels([["rId1", "slideMaster", "../slideMasters/slideMaster1.xml"]]),
    "ppt/slides/slide1.xml": slide1,
    "ppt/slides/_rels/slide1.xml.rels": rels([["rId1", "slideLayout", "../slideLayouts/slideLayout1.xml"], ["rId9", "hyperlink", "https://example.com/portal", true]]),
    "ppt/slides/slide2.xml": slide2,
    "ppt/slides/_rels/slide2.xml.rels": rels([["rId1", "slideLayout", "../slideLayouts/slideLayout1.xml"], ["rId2", "notesSlide", "../notesSlides/notesSlide1.xml"]]),
    "ppt/notesMasters/notesMaster1.xml": notesMaster,
    "ppt/notesMasters/_rels/notesMaster1.xml.rels": rels([["rId1", "theme", "../theme/theme2.xml"]]),
    "ppt/notesSlides/notesSlide1.xml": notes,
    "ppt/notesSlides/_rels/notesSlide1.xml.rels": rels([["rId1", "notesMaster", "../notesMasters/notesMaster1.xml"], ["rId2", "slide", "../slides/slide2.xml"]]),
    "ppt/theme/theme1.xml": T.THEME,
    "ppt/theme/theme2.xml": T.THEME,
  };
  fs.writeFileSync(target, zipSync(Object.fromEntries(Object.entries(files).map(([name, text]) => [name, strToU8(text)]))));
  return target;
}

const SPEC = {
  properties: { title: "Q3 supplier review", author: "Operations" },
  footer: "Q3 supplier review",
  slideNumbers: true,
  slides: [
    { title: "Q3 supplier review", subtitle: "Operations · 04 September" },
    { title: "Summary", body: ["Control-module ETA moved from 21 Aug to 29 Aug", { text: "It is now eight days late", items: ["Line 2", "Line 4"] }], notes: "Give the new date first." },
    { title: "Late parts", left: { text: ["Acme", "Bolt Co"], bullets: false }, right: { image: "logo.png", alt: "Supplier logo" } },
    { title: "Days late", body: { table: [["Supplier", "Days late"], ["Acme", 8], [{ text: "Bolt Co", bold: true, fill: "#FDE68A" }, 3]], widths: [3, 1] } },
    { title: "Trend", body: { chart: "column", title: "Deliveries", categories: ["Jul", "Aug", "Sep"], series: [{ name: "Late", values: [2, 5, 8] }, { name: "On time", values: [9, 7, 4] }], labels: true } },
    { section: "Next steps" },
    { layout: "Blank", background: "#F3F4F6", shapes: [{ text: [[{ text: "Call ", bold: true }, { text: "Acme", link: "https://example.com/acme" }]], x: 2, y: 2, w: 12, h: 2, size: 24 }, { shape: "rounded", text: "Owner: Priya", fill: "#2563EB", color: "#FFFFFF", x: 2, y: 5, w: 8, h: 2 }, { image: "logo.png", x: 20, y: 2, w: 4 }] },
  ],
};

function created() {
  const target = file("created.pptx");
  if (!fs.existsSync(target)) {
    fs.writeFileSync(file("logo.png"), png(400, 200));
    const spec = JSON.parse(JSON.stringify(SPEC).replaceAll("logo.png", file("logo.png")));
    cli("create", writeJson("spec.json", spec), target);
  }
  return target;
}

test("the three skills share one ooxml.cjs", () => {
  const read = (name) => fs.readFileSync(path.join(__dirname, "..", name, "scripts", "lib", "ooxml.cjs"), "utf8");
  assert.equal(read("pptx"), read("docx"));
  assert.equal(read("pptx"), read("xlsx"));
});

test("create writes a deck that reads back slide by slide", () => {
  const target = created();
  assertWellFormed(target);
  assertRelsResolve(target);
  const facts = json("info", target);
  assert.deepEqual(facts.size, { w: 33.87, h: 19.05, ratio: "16:9" });
  assert.equal(facts.properties.title, "Q3 supplier review");
  assert.equal(facts.properties.author, "Operations");
  assert.deepEqual(facts.slides.map((s) => s.layout), ["Title Slide", "Title and Content", "Two Content", "Title and Content", "Title and Content", "Section Header", "Blank"]);
  assert.deepEqual(facts.slides.map((s) => s.title ?? null), ["Q3 supplier review", "Summary", "Late parts", "Days late", "Trend", "Next steps", null]);
  assert.equal(facts.slides[1].notes, true);
  assert.equal(facts.slides[3].tables, 1);
  assert.equal(facts.slides[4].charts, 1);
  assert.equal(facts.slides[6].pictures, 1);

  const md = cli("read", target);
  assert.match(md, /## Slide 2: Summary\n\(layout "Title and Content"\)\n\n- Control-module ETA moved from 21 Aug to 29 Aug\n- It is now eight days late\n {2}- Line 2\n {2}- Line 4/);
  assert.match(md, /Notes:\n> Give the new date first\./);
  assert.match(md, /\nAcme\nBolt Co\n/);
  assert.match(md, /!\[Supplier logo\]\(ppt\/media\/image1\.png\)/);
  assert.match(md, /\| Supplier \| Days late \|\n\| --- \| --- \|\n\| Acme \| 8 \|\n\| Bolt Co \| 3 \|/);
  assert.match(md, /Chart: column, "Deliveries"\n\n\| {2}\| Late \| On time \|/);
  assert.match(md, /\*\*Call\*\* \[Acme\]\(https:\/\/example\.com\/acme\)/);
  assert.doesNotMatch(md, /Slide Number/);

  const p = parts(target);
  assert.equal(Object.keys(p).filter((name) => /^ppt\/media\//.test(name)).length, 1, "one copy of an image used twice");
  const slide3 = p["ppt/slides/slide3.xml"];
  assert.match(slide3, /<p:ph idx="2" sz="half"\/>|<p:ph sz="half" idx="2"\/>/);
  assert.match(slide3, /<a:buNone\/>/);
  const slide7 = p["ppt/slides/slide7.xml"];
  assert.match(slide7, /<p:bg><p:bgPr><a:solidFill><a:srgbClr val="F3F4F6"\/>/);
  assert.match(slide7, /<a:ext cx="1440000" cy="720000"\/>/, "a 4 cm wide picture of a 2:1 image is 2 cm high");
  assert.match(p["ppt/slides/slide2.xml"], /<a:fld [^>]*type="slidenum"[^>]*><a:rPr lang="en-US"\/><a:t>2<\/a:t>/);
  assert.match(p["ppt/slides/slide4.xml"], /<a:tcPr anchor="ctr"><a:solidFill><a:srgbClr val="FDE68A"\/>/);
  const book = unzipSync(p["ppt/embeddings/Microsoft_Excel_Worksheet1.xlsx"]);
  assert.match(strFromU8(book["xl/worksheets/sheet1.xml"]), /Sep.*<v>8<\/v><\/c><c r="C4"><v>4<\/v>/);

  const data = json("read", target, "--format", "json", "--slides", "5");
  assert.equal(data.slides.length, 1);
  const chart = data.slides[0].shapes.find((shape) => shape.kind === "chart").chart;
  assert.deepEqual(chart.categories, ["Jul", "Aug", "Sep"]);
  assert.deepEqual(chart.series, [{ name: "Late", values: [2, 5, 8] }, { name: "On time", values: [9, 7, 4] }]);
});

test("slide size, template content type and 4:3 decks", () => {
  const target = file("square.potx");
  cli("create", writeJson("square.json", { size: "4:3", slides: [{ title: "Only" }] }), target);
  assert.deepEqual(json("info", target).size, { w: 25.4, h: 19.05, ratio: "4:3" });
  assert.match(parts(target)["[Content_Types].xml"], /presentationml\.template\.main\+xml/);
  const custom = file("custom.pptx");
  cli("create", writeJson("custom.json", { size: { w: 20, h: 10 }, slides: [] }), custom);
  assert.deepEqual(json("info", custom).size, { w: 20, h: 10 });
});

test("edit changes text, tables, charts, pictures and slides", () => {
  const source = created();
  fs.writeFileSync(file("wide.png"), png(300, 100));
  const out = file("edited.pptx");
  const result = json("edit", source, writeJson("ops.json", [
    { op: "replace", find: "21 Aug", with: "22 Aug" },
    { op: "setText", slide: 2, shape: "body", text: ["New first point", { text: "Detail", level: 1 }] },
    { op: "setText", slide: 1, shape: "title", text: "Q3 supplier review, revised" },
    { op: "cell", slide: 4, row: 2, column: 2, text: "9" },
    { op: "addRow", slide: 4, cells: ["Cog Ltd", "1"] },
    { op: "deleteRow", slide: 4, row: 3 },
    { op: "chart", slide: 5, categories: ["Q1", "Q2"], series: [{ name: "A", values: [1, 2] }, { name: "B", values: [3, 4] }, { name: "C", values: [5, null] }] },
    { op: "image", slide: 3, shape: "Picture 3", image: file("wide.png"), alt: "Wide logo" },
    { op: "notes", slide: 2, text: "New notes" },
    { op: "hide", slide: 6 },
    { op: "properties", title: "Revised review" },
  ]), out);
  assert.equal(result.results[0].replaced, 1);
  assertWellFormed(out);
  assertRelsResolve(out);
  const md = cli("read", out);
  assert.match(md, /## Slide 1: Q3 supplier review, revised/);
  assert.match(md, /- New first point\n {2}- Detail/);
  assert.doesNotMatch(md, /21 Aug/);
  assert.match(md, /\| Acme \| 9 \|\n\| Cog Ltd \| 1 \|/);
  assert.doesNotMatch(md, /Bolt Co \| 3/);
  assert.match(md, /\| Q2 \| 2 \| 4 \| {2}\|/);
  assert.match(md, /Notes:\n> New notes/);
  assert.match(md, /## Slide 6: Next steps\n\(layout "Section Header", hidden\)/);
  assert.match(md, /!\[Wide logo\]/);
  assert.equal(json("info", out).properties.title, "Revised review");
  const p = parts(out);
  const book = strFromU8(unzipSync(p["ppt/embeddings/Microsoft_Excel_Worksheet1.xlsx"])["xl/worksheets/sheet1.xml"]);
  assert.match(book, /Q2/);
  assert.doesNotMatch(book, /Sep/);
  const chartXml = p["ppt/charts/chart1.xml"];
  assert.equal((chartXml.match(/<c:ser>/g) ?? []).length, 3);
  assert.match(chartXml, /<c:order val="2"\/>/);
  assert.match(p["ppt/slides/slide3.xml"], /<a:srcRect [^>]*\/>/, "the wider image is cropped to its frame");
  assert.ok(!Object.keys(p).some((name) => name === "ppt/media/image1.png") || p["ppt/slides/_rels/slide7.xml.rels"].includes("image1.png"), "the old image stays only while a slide uses it");
  assert.match(p["ppt/slides/slide4.xml"], /<a:solidFill><a:srgbClr val="FDE68A"\/><\/a:solidFill><\/a:tcPr><\/a:tc><a:tc>/);
  const title = /<p:sp>.*?<\/p:sp>/s.exec(p["ppt/slides/slide1.xml"])[0];
  assert.match(title, /revised/);
});

test("slide operations keep sections, notes, charts and the package consistent", () => {
  const source = created();
  const out = file("reordered.pptx");
  json("edit", source, writeJson("slides.json", [
    { op: "duplicate", slide: 5 },
    { op: "chart", slide: 6, categories: ["X"], series: [{ name: "Only", values: [1] }] },
    { op: "duplicate", slide: 2, position: 1 },
    { op: "move", slide: 8, to: 2 },
    { op: "delete", slide: [4, 9] },
    { op: "addSlide", after: 1, layout: "Title Only", title: "Inserted" },
  ]), out);
  assertWellFormed(out);
  assertRelsResolve(out);
  const facts = json("info", out);
  assert.deepEqual(facts.slides.map((s) => s.title ?? null), ["Summary", "Inserted", "Next steps", "Q3 supplier review", "Late parts", "Days late", "Trend", "Trend"]);
  assert.equal(facts.slides[0].notes, true, "a duplicate keeps the notes");
  const charts = json("read", out, "--format", "json", "--slides", "7-8").slides.map((s) => s.shapes.find((shape) => shape.kind === "chart").chart);
  assert.deepEqual(charts[0].categories, ["Jul", "Aug", "Sep"], "the original chart keeps its data");
  assert.deepEqual(charts[1].categories, ["X"], "the copy has its own chart");
  assert.notEqual(charts[0].part, charts[1].part);
  const p = parts(out);
  assert.equal(Object.keys(p).filter((name) => /^ppt\/slides\/slide\d+\.xml$/.test(name)).length, 8, "deleted slides leave the package");
  assert.equal(Object.keys(p).filter((name) => /^ppt\/notesSlides\/notesSlide\d+\.xml$/.test(name)).length, 1, "the deleted slide's notes leave the package");
  assert.equal(new Set([...p["ppt/presentation.xml"].matchAll(/<p:sldId id="(\d+)"/g)].map((m) => m[1])).size, 8);
});

test("edit keeps the parts of a foreign deck that it does not change", () => {
  const source = foreignDeck(file("foreign.pptx"));
  const facts = json("info", source);
  assert.deepEqual(facts.size, { w: 25.4, h: 19.05, ratio: "4:3" });
  assert.equal(facts.slides[0].title, "The supplier’s delivery date is 21 Aug.");
  const md = cli("read", source, "--ids");
  assert.match(md, /\[#2 Titel 1, title\] The supplier’s \*\*delivery date is\*\* 21 Aug\./);
  assert.match(md, /Prepared for \{\{client\}\} \[portal\]\(https:\/\/example\.com\/portal\)/);
  assert.match(md, /- Welcome \{\{client\}\}\n {2}- Second level/);
  assert.match(md, /> Greet \{\{client\}\} first\./);

  assert.match(cliFails("edit", source, writeJson("ambiguous.json", [{ op: "replace", find: "{{client}}", with: "Acme" }]), file("x.pptx")), /occurs 2 times \(slide 1, slide 2\)/);
  const out = file("foreign-edited.pptx");
  const result = json("edit", source, writeJson("foreign.json", [
    { op: "replace", find: "supplier's delivery date is 21", with: "supplier's delivery date is 29" },
    { op: "replace", find: "{{client}}", with: "Acme & Co", all: true, notes: true },
    { op: "cell", slide: 2, row: 2, column: 1, text: "Mutter" },
    { op: "addShape", slide: 2, text: "Draft", x: 1, y: 1, w: 3, h: 1, color: "#C00000" },
  ]), out);
  assert.equal(result.results[1].replaced, 3);
  assertWellFormed(out);
  assertRelsResolve(out);
  const after = cli("read", out);
  assert.match(after, /## Slide 1: The supplier's delivery date is 29 Aug\./);
  assert.match(after, /Prepared for Acme & Co \[portal\]/);
  assert.match(after, /> Greet Acme & Co first\./);
  assert.match(after, /\| Mutter \| 4,10 \|/);
  const before = parts(source);
  const p = parts(out);
  for (const name of ["customXml/item1.xml", "docProps/custom.xml", "ppt/slideLayouts/slideLayout2.xml", "ppt/slideMasters/slideMaster1.xml", "ppt/theme/theme2.xml"]) assert.equal(p[name], before[name], `${name} is unchanged`);
  assert.match(p["ppt/slides/slide1.xml"], /<a:p><a:r><a:rPr lang="de-DE"\/><a:t>The supplier&apos;s delivery date is 29<\/a:t><\/a:r><a:r><a:rPr lang="de-DE"\/><a:t> Aug\.<\/a:t><\/a:r><\/a:p>/, "the new text takes the format of the first character it replaces");
  assert.match(p["ppt/slides/slide2.xml"], /<a:rPr lang="de-DE" sz="1400" dirty="0"><a:solidFill><a:srgbClr val="C00000"\/><\/a:solidFill><\/a:rPr><a:t>Mutter<\/a:t>/, "a new cell text keeps the old format");
  assert.match(p["ppt/slides/slide2.xml"], /<p:cNvPr id="5" name="TextBox 4"\/>/, "a new shape takes the next free id");
  assert.match(p["ppt/presentation.xml"], /p14:section name="Start"/);
  assert.match(p["ppt/slides/_rels/slide1.xml.rels"], /https:\/\/example\.com\/portal/);
  assert.match(p["ppt/slides/slide1.xml"], /<p:timing>/);

  const unanimated = json("edit", source, writeJson("del-title.json", [{ op: "deleteShape", slide: 1, shape: "title" }]), file("del-title.pptx"));
  assert.equal(unanimated.results[0].notes, undefined);
  assert.match(parts(file("del-title.pptx"))["ppt/slides/slide1.xml"], /<p:timing>/);
  const animated = json("edit", source, writeJson("del-group.json", [{ op: "deleteShape", slide: 1, shape: 7 }]), file("del-group.pptx"));
  assert.match(animated.results[0].notes[0], /animations were removed/);
  const slide1 = parts(file("del-group.pptx"))["ppt/slides/slide1.xml"];
  assert.doesNotMatch(slide1, /<p:timing>|Textfeld/);
  assert.doesNotMatch(parts(file("del-group.pptx"))["ppt/slides/_rels/slide1.xml.rels"], /portal/, "the link of the deleted shape goes too");
});

test("a template gives its layouts and placeholders to a new deck", () => {
  const template = foreignDeck(file("brand.pptx"));
  const layouts = json("layouts", template);
  assert.deepEqual(layouts.map((l) => l.name), ["Titel und Inhalt", "Kundenfolie"]);
  assert.deepEqual(layouts[1].placeholders.map((ph) => [ph.idx, ph.type, ph.name]), [[0, "title", "Titel 1"], [13, "body", "Kunde"], [14, "pic", "Logo"]]);
  assert.deepEqual(layouts[0].placeholders[1].box, { x: 1.27, y: 4.45, w: 22.86, h: 12.57 }, "a layout placeholder without a frame takes the master's");
  fs.writeFileSync(file("logo.png"), png(400, 200));
  const out = file("from-template.pptx");
  json("create", writeJson("tpl.json", {
    template,
    properties: { title: "Acme review" },
    slides: [
      { layout: "Kundenfolie", title: "Acme", placeholders: { 13: "Acme & Co, Hamburg", Logo: { image: file("logo.png") } } },
      { layout: "titel und inhalt", title: "Agenda", body: ["One", "Two"] },
    ],
  }), out);
  assertWellFormed(out);
  assertRelsResolve(out);
  const facts = json("info", out);
  assert.deepEqual(facts.slides.map((s) => [s.layout, s.title]), [["Kundenfolie", "Acme"], ["Titel und Inhalt", "Agenda"]]);
  const kunde = json("read", out, "--format", "json", "--slides", "1").slides[0].shapes.find((shape) => shape.placeholder === "body 13");
  assert.deepEqual(kunde.box, { x: 1.27, y: 4.45, w: 11.11, h: 2.22 }, "a slide placeholder without a frame takes the layout's");
  assert.equal(kunde.paragraphs[0].text, "Acme & Co, Hamburg");
  const p = parts(out);
  assert.equal(p["customXml/item1.xml"], CUSTOM_XML);
  assert.ok(!Object.values(p).some((body) => typeof body === "string" && body.includes("{{client}}")), "the template's own slides and notes are gone");
  assert.doesNotMatch(p["ppt/presentation.xml"], /sectionLst/, "the template's sections go with its slides");
  const pic = Object.entries(p).find(([name, body]) => /^ppt\/slides\/slide\d+\.xml$/.test(name) && typeof body === "string" && body.includes("<p:pic>"))[1];
  assert.match(pic, /<p:ph type="pic" idx="14" sz="quarter"\/>|<p:ph type="pic" sz="quarter" idx="14"\/>/);
  assert.match(pic, /<a:srcRect l="\d+" r="\d+"\/>/, "a wide image fills a square picture placeholder with a crop");
  assert.match(pic, /<a:off x="5000000" y="1600200"\/><a:ext cx="3000000" cy="3000000"\/>/, "the picture takes the layout placeholder's frame");
  assert.match(cliFails("create", writeJson("bad-layout.json", { template, slides: [{ layout: "Nope" }] }), file("x.pptx")), /no layout named "Nope"; the layouts are: 1 "Titel und Inhalt", 2 "Kundenfolie"/);
  assert.match(cliFails("create", writeJson("bad-ph.json", { template, slides: [{ layout: 2, placeholders: { 99: "x" } }] }), file("x.pptx")), /no placeholder "99" in the layout; it has 0 "Titel 1" \(title\), 13 "Kunde" \(body\), 14 "Logo" \(pic\)/);

  const potx = file("brand.potx");
  json("create", writeJson("to-potx.json", { template, keepSlides: true, slides: [] }), potx);
  assert.match(parts(potx)["[Content_Types].xml"], /presentationml\.template\.main\+xml/);
  const fromPotx = file("from-potx.pptx");
  json("create", writeJson("from-potx.json", { template: potx, slides: [{ title: "Hi", body: "There" }] }), fromPotx);
  assert.match(parts(fromPotx)["[Content_Types].xml"], /presentationml\.presentation\.main\+xml/);
  assert.equal(json("info", fromPotx).slides.length, 1);
});

test("a macro deck saved as .pptx loses its macros and says so", () => {
  const source = parts(created());
  const files = { ...source, "ppt/vbaProject.bin": new Uint8Array([1, 2, 3]) };
  files["ppt/_rels/presentation.xml.rels"] = source["ppt/_rels/presentation.xml.rels"].replace("</Relationships>", '<Relationship Id="rId99" Type="http://schemas.microsoft.com/office/2006/relationships/vbaProject" Target="vbaProject.bin"/></Relationships>');
  files["[Content_Types].xml"] = source["[Content_Types].xml"].replace("presentationml.presentation.main+xml", "presentationml.presentation.main+xml").replace("<Default Extension=\"xml\"", '<Default Extension="bin" ContentType="application/vnd.ms-office.vbaProject"/><Default Extension="xml"');
  fs.writeFileSync(file("macro.pptm"), zipSync(Object.fromEntries(Object.entries(files).map(([name, body]) => [name, typeof body === "string" ? strToU8(body) : body]))));
  assert.equal(json("info", file("macro.pptm")).macros, true);
  const kept = json("edit", file("macro.pptm"), writeJson("none.json", []), file("kept.pptm"));
  assert.equal(kept.notes, undefined);
  assert.ok("ppt/vbaProject.bin" in parts(file("kept.pptm")));
  const dropped = json("edit", file("macro.pptm"), writeJson("none.json", []), file("dropped.pptx"));
  assert.match(dropped.notes[0], /macros were removed/);
  assert.ok(!("ppt/vbaProject.bin" in parts(file("dropped.pptx"))));
});

test("bad input is refused with a message", () => {
  const source = created();
  const fails = (ops) => cliFails("edit", source, writeJson("bad.json", ops), file("bad.pptx"));
  assert.match(fails([{ op: "explode" }]), /unknown op "explode"/);
  assert.match(fails([{ op: "notes", slide: 99, text: "x" }]), /slide must be 1 to 7, got 99/);
  assert.match(fails([{ op: "replace", find: "no such words", with: "x" }]), /no text "no such words" found/);
  assert.match(fails([{ op: "setText", slide: 3, shape: "Picture 3", text: "x" }]), /holds no text/);
  assert.match(fails([{ op: "cell", slide: 2, row: 1, column: 1, text: "x" }]), /slide 2 has 0 tables/);
  assert.match(fails([{ op: "chart", slide: 4, categories: ["a"], series: [{ values: [1] }] }]), /slide 4 has 0 charts/);
  assert.match(fails([{ op: "chart", slide: 5, categories: ["a", "b"], series: [{ values: [1] }] }]), /series 1 has 1 values for 2 categories/);
  assert.match(fails([{ op: "deleteShape", slide: 2, shape: 42 }]), /slide 2 has no shape 42; its shapes are 2 "Title 1" \(title\)/);
  assert.match(fails([{ op: "replace", find: "Acme", with: "a\nb" }]), /line break/);
  assert.match(cliFails("create", writeJson("bad-slide.json", { slides: [{ titel: "x" }] }), file("x.pptx")), /slide 1: unknown slide field "titel"/);
  assert.match(cliFails("create", writeJson("bad-two.json", { slides: [{ layout: "Title Only", body: "x" }] }), file("x.pptx")), /no placeholder for body content/);
  assert.match(cliFails("create", writeJson("bad-size.json", { size: "21:9", slides: [] }), file("x.pptx")), /size must be 16:9, 4:3, 16:10/);
  assert.match(cliFails("create", writeJson("ok.json", { slides: [] }), file("x.ppt")), /output must end in \.pptx/);
  fs.writeFileSync(file("old.ppt"), "x");
  assert.match(cliFails("info", file("old.ppt")), /\.ppt files are not supported/);
  assert.match(cliFails("read", source, "--slides", "9"), /slides must be 1 to 7/);
  assert.ok(!fs.existsSync(file("bad.pptx")), "a failed edit writes no file");
});

test("read pages a long deck", () => {
  const spec = { slides: Array.from({ length: 40 }, (_, i) => ({ title: `Slide ${i + 1}`, body: Array.from({ length: 8 }, (_, j) => `Point ${j + 1} ${"text ".repeat(20)}`) })) };
  const target = file("long.pptx");
  cli("create", writeJson("long.json", spec), target);
  const first = cli("read", target, "--max-chars", "8000");
  const stop = /The output stops at slide (\d+) of 40\. Run read with --slides (\d+)- to see the rest\./.exec(first);
  assert.ok(stop, first.slice(-200));
  assert.equal(Number(stop[2]), Number(stop[1]) + 1);
  assert.match(cli("read", target, "--slides", `${stop[2]}-`), new RegExp(`^## Slide ${stop[2]}: Slide ${stop[2]}`));
});

test("python-pptx opens the created and edited files", (t) => {
  const probe = spawnSync("python3", ["-I", "-c", "import pptx"], { encoding: "utf8" });
  if (probe.status !== 0) {
    t.skip("python-pptx is not installed");
    return;
  }
  const script = [
    "import sys",
    "from pptx import Presentation",
    "for f in sys.argv[1:]:",
    "    p = Presentation(f)",
    "    for s in p.slides:",
    "        for sh in s.shapes:",
    "            if sh.has_text_frame: sh.text_frame.text",
    "            if sh.has_table: [c.text for r in sh.table.rows for c in r.cells]",
    "            if sh.has_chart: [list(x.values) for x in sh.chart.plots[0].series]",
    "        if s.has_notes_slide: s.notes_slide.notes_text_frame.text",
    "    print(f, len(p.slides))",
  ].join("\n");
  const files = [created(), file("edited.pptx"), file("reordered.pptx"), file("foreign-edited.pptx"), file("from-template.pptx")].filter((name) => fs.existsSync(name));
  const result = spawnSync("python3", ["-I", "-c", script, ...files], { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim().split("\n").length, files.length);
});
