"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { execFileSync, spawnSync } = require("node:child_process");
const { zipSync, unzipSync, strToU8, strFromU8 } = require("fflate");
const { Workbook } = require("../xlsx/scripts/lib/workbook.cjs");
const { recalculate } = require("../xlsx/scripts/lib/recalc.cjs");
const refs = require("../xlsx/scripts/lib/refs.cjs");

const CLI = path.join(__dirname, "..", "xlsx", "scripts", "xlsx.cjs");
const dir = fs.mkdtempSync(path.join(os.tmpdir(), "xlsx-skill-"));
test.after(() => fs.rmSync(dir, { recursive: true, force: true }));

function cli(...args) {
  return execFileSync(process.execPath, [CLI, ...args], { encoding: "utf8" });
}

function writeJson(name, value) {
  const file = path.join(dir, name);
  fs.writeFileSync(file, JSON.stringify(value));
  return file;
}

function parts(file) {
  return Object.fromEntries(Object.entries(unzipSync(new Uint8Array(fs.readFileSync(file)))).map(([name, bytes]) => [name, strFromU8(bytes)]));
}

const HEAD = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n';
const MAIN = 'xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"';
const REL = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";

function foreignWorkbook(file) {
  const files = {
    "[Content_Types].xml": `${HEAD}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/><Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/><Override PartName="/xl/calcChain.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.calcChain+xml"/><Override PartName="/docProps/custom.xml" ContentType="application/vnd.openxmlformats-officedocument.custom-properties+xml"/></Types>`,
    "_rels/.rels": `${HEAD}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/officeDocument" Target="xl/workbook.xml"/><Relationship Id="rId2" Type="${REL}/custom-properties" Target="docProps/custom.xml"/></Relationships>`,
    "docProps/custom.xml": `${HEAD}<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties"><property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="2" name="Owner"><vt:lpwstr xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">Finance</vt:lpwstr></property></Properties>`,
    "xl/workbook.xml": `${HEAD}<workbook ${MAIN}><workbookPr/><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets><definedNames><definedName name="Total">Data!$C$4</definedName></definedNames><calcPr calcId="191029"/></workbook>`,
    "xl/_rels/workbook.xml.rels": `${HEAD}<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="${REL}/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="${REL}/sharedStrings" Target="sharedStrings.xml"/><Relationship Id="rId3" Type="${REL}/styles" Target="styles.xml"/><Relationship Id="rId4" Type="${REL}/calcChain" Target="calcChain.xml"/></Relationships>`,
    "xl/sharedStrings.xml": `${HEAD}<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="3" uniqueCount="3"><si><t>Item</t></si><si><r><t>Unit </t></r><r><rPr><b/></rPr><t>price</t></r></si><si><t>Line total</t></si></sst>`,
    "xl/styles.xml": `${HEAD}<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="165" formatCode="dd/mm/yyyy"/></numFmts><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles><dxfs count="1"><dxf><font><b/></font></dxf></dxfs></styleSheet>`,
    "xl/calcChain.xml": `${HEAD}<calcChain xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><c r="C2" i="1"/></calcChain>`,
    "xl/worksheets/sheet1.xml": `${HEAD}<worksheet ${MAIN}><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c><c r="D1" s="1"><v>46053</v></c></row><row r="2"><c r="A2"><v>2</v></c><c r="B2"><v>10</v></c><c r="C2"><f t="shared" ref="C2:C3" si="0">A2*B2</f><v>20</v></c></row><row r="3"><c r="A3"><v>3</v></c><c r="B3"><v>5</v></c><c r="C3"><f t="shared" si="0"/><v>15</v></c></row><row r="4"><c r="C4"><f>SUM(C2:C3)</f><v>35</v></c></row></sheetData><conditionalFormatting sqref="C2:C3"><cfRule type="cellIs" dxfId="0" priority="1" operator="greaterThan"><formula>18</formula></cfRule></conditionalFormatting></worksheet>`,
  };
  fs.writeFileSync(file, zipSync(Object.fromEntries(Object.entries(files).map(([name, text]) => [name, strToU8(text)]))));
  return file;
}

test("references shift, rename and keep strings intact", () => {
  assert.equal(refs.shiftFormula("A2*$B2+B$1+'My Sheet'!C3", 2, 1), "B4*$B4+C$1+'My Sheet'!D5");
  assert.equal(refs.shiftFormula('SUM(A1:A3)&"A1"+LOG10(A1)', 1, 0), 'SUM(A2:A4)&"A1"+LOG10(A2)');
  assert.equal(refs.shiftFormula("SUM($5:$5)+SUM(B:B)", 3, 1), "SUM($5:$5)+SUM(C:C)");
  assert.equal(refs.renameSheetInFormula("Old!A1+'old'!B2+\"Old!A1\"", "Old", "New Name"), "'New Name'!A1+'New Name'!B2+\"Old!A1\"");
  assert.equal(refs.toFileFormula('=XLOOKUP(A1,B:B,C:C)+STDEV.S(D1:D3)&"IFS("'), '_xlfn.XLOOKUP(A1,B:B,C:C)+_xlfn.STDEV.S(D1:D3)&"IFS("');
  assert.equal(refs.fromFileFormula("_xlfn.XLOOKUP(A1,B:B,C:C)"), "XLOOKUP(A1,B:B,C:C)");
  assert.deepEqual([...refs.sheetsInFormula("SUM(Data!A1:A2)+'Q1 Plan'!B1")].sort(), ["data", "q1 plan"]);
});

test("create writes values, formulas, styles, names and dates, and reads them back", () => {
  const out = path.join(dir, "created.xlsx");
  const spec = writeJson("spec.json", {
    sheets: [
      {
        name: "Sales",
        rows: [
          ["Region", "Q1", "Q2", "Total"],
          ["North", 120, 135, "=SUM(B2:C2)"],
          ["South <&> \"East\"", 80.5, null, "=B3+C3"],
          ["Total", "=SUM(B2:B3)", "=SUM(C2:C3)", "=SUM(D2:D3)"],
          ["Stats", "=MAX(B2:C3)", '=XLOOKUP("North",A2:A3,C2:C3)', "=ROUND(STDEV.S(B2:B3),2)"],
        ],
        columns: { A: 22 },
        freeze: "A2",
        merge: ["F1:G1"],
        styles: [{ range: "A1:D1", style: { bold: true, fill: "#DDEBF7", border: "thin" } }, { range: "B2:D4", style: { numFmt: "#,##0.00" } }],
      },
      { name: "Inputs", rows: [["Rate", 0.05], ["Start", { date: "2026-01-31" }], ["Note", { text: "=literal" }], ["Multi", "  two\nlines "]] },
    ],
    names: [{ name: "Rate", ref: "Inputs!$B$1" }],
    ops: [{ op: "set", sheet: "Sales", cell: "F2", formula: "Rate*D4" }, { op: "set", sheet: "Sales", cell: "F3", value: "=EDATE(Inputs!B2,1)", style: { numFmt: "yyyy-mm-dd" } }],
  });
  const summary = JSON.parse(cli("create", spec, out));
  assert.deepEqual(summary.sheets, ["Sales", "Inputs"]);
  assert.deepEqual(summary.errors, []);
  assert.deepEqual(summary.unsupported, []);

  const book = Workbook.open(new Uint8Array(fs.readFileSync(out)));
  const sales = book.sheet("sales");
  assert.equal(sales.cell("D2").value, 255);
  assert.equal(sales.cell("D2").formula, "SUM(B2:C2)");
  assert.equal(sales.cell("D4").value, 335.5);
  assert.equal(sales.cell("B5").value, 135);
  assert.equal(sales.cell("C5").value, 135);
  assert.equal(sales.cell("D5").value, 27.93);
  assert.equal(sales.cell("F2").value, 16.775);
  assert.equal(sales.cell("F3").value, "2026-02-28");
  assert.equal(sales.cell("A3").value, 'South <&> "East"');
  assert.equal(book.styles.formatCode(sales.cell("B2").style), "#,##0.00");
  const inputs = book.sheet("Inputs");
  assert.equal(inputs.cell("B2").value, "2026-01-31");
  assert.equal(inputs.cell("B3").value, "=literal");
  assert.equal(inputs.cell("B3").formula, undefined);
  assert.equal(inputs.cell("B4").value, "  two\nlines ");
  assert.deepEqual(book.definedNames(), [{ name: "Rate", ref: "Inputs!$B$1" }]);

  const xml = parts(out);
  assert.match(xml["xl/worksheets/sheet1.xml"], /<f>_xlfn\.XLOOKUP\(/);
  assert.match(xml["xl/worksheets/sheet1.xml"], /<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"\/>/);
  assert.match(xml["xl/worksheets/sheet1.xml"], /<mergeCell ref="F1:G1"\/>/);
  assert.match(xml["xl/worksheets/sheet1.xml"], /<col min="1" max="1" width="22" customWidth="1"\/>/);
  assert.match(xml["xl/workbook.xml"], /fullCalcOnLoad="1"/);
  assert.match(xml["xl/styles.xml"], /<b\/>/);

  const markdown = cli("read", out, "--range", "A1:D2");
  assert.match(markdown, /\| 2 \| North \| 120 \| 135 \| 255 \|/);
  const csv = cli("read", out, "--format", "csv", "--formulas", "--range", "A3:D3");
  assert.equal(csv.trim(), '"South <&> ""East""",80.5,,=B3+C3');
  const json = JSON.parse(cli("read", out, "--sheet", "Sales", "--format", "json", "--max-rows", "2"));
  assert.deepEqual(json.truncated, { shown: 2, total: 5 });
});

test("edit keeps unknown parts, expands shared formulas and recalculates", () => {
  const source = foreignWorkbook(path.join(dir, "foreign.xlsx"));
  const before = Workbook.open(new Uint8Array(fs.readFileSync(source)));
  assert.equal(before.sheet("Data").cell("B1").value, "Unit price");
  assert.equal(before.sheet("Data").cell("D1").value, "2026-01-31");
  assert.equal(before.sheet("Data").cell("C3").value, 15);

  const out = path.join(dir, "foreign-edited.xlsx");
  const ops = writeJson("ops.json", [
    { op: "set", sheet: "Data", cell: "B3", value: 7 },
    { op: "append", sheet: "Data", rows: [[4, 2.5, "=A5*B5"]] },
    { op: "set", sheet: "Data", cell: "C4", formula: "SUM(C2:C3)+C5" },
    { op: "addSheet", name: "Summary" },
    { op: "set", sheet: "Summary", cell: "A1", formula: "Data!C4*2" },
    { op: "set", sheet: "Summary", cell: "A2", formula: "Total" },
    { op: "renameSheet", from: "Data", to: "Line items" },
    { op: "style", sheet: "Line items", range: "A1:C1", style: { bold: true } },
    { op: "columns", sheet: "Summary", widths: { A: 14 } },
  ]);
  const summary = JSON.parse(cli("edit", source, ops, out));
  assert.deepEqual(summary.errors, []);
  assert.deepEqual(summary.sheets, ["Line items", "Summary"]);

  const book = Workbook.open(new Uint8Array(fs.readFileSync(out)));
  const data = book.sheet("Line items");
  assert.equal(data.cell("C3").formula, "A3*B3");
  assert.equal(data.cell("C3").value, 21);
  assert.equal(data.cell("C5").value, 10);
  assert.equal(data.cell("C4").value, 51);
  assert.equal(data.cell("A1").value, "Item");
  assert.equal(book.sheet("Summary").cell("A1").formula, "'Line items'!C4*2");
  assert.equal(book.sheet("Summary").cell("A1").value, 102);
  assert.equal(book.sheet("Summary").cell("A2").value, 51);
  assert.deepEqual(book.definedNames(), [{ name: "Total", ref: "'Line items'!$C$4" }]);

  const xml = parts(out);
  assert.equal(xml["xl/calcChain.xml"], undefined);
  assert.doesNotMatch(xml["[Content_Types].xml"], /calcChain/);
  assert.doesNotMatch(xml["xl/_rels/workbook.xml.rels"], /calcChain/);
  assert.match(xml["docProps/custom.xml"], /Finance/);
  assert.match(xml["xl/worksheets/sheet1.xml"], /<conditionalFormatting sqref="C2:C3">/);
  assert.match(xml["xl/sharedStrings.xml"], /Line total/);
  assert.match(xml["xl/worksheets/sheet1.xml"], /<c r="A1" t="s" s="\d+"><v>0<\/v><\/c>/);
});

test("delete refuses a sheet that formulas use, and works once they are gone", () => {
  const source = path.join(dir, "two.xlsx");
  cli("create", writeJson("two.json", { sheets: [{ name: "A", rows: [[1]] }, { name: "B", rows: [["=A!A1+1"]] }] }), source);
  const blocked = spawnSync(process.execPath, [CLI, "edit", source, writeJson("del.json", [{ op: "deleteSheet", name: "A" }]), path.join(dir, "x.xlsx")], { encoding: "utf8" });
  assert.equal(blocked.status, 1);
  assert.match(blocked.stderr, /op 1 \(deleteSheet\): cannot delete "A": B!A1 refers to it/);

  const out = path.join(dir, "one.xlsx");
  cli("edit", source, writeJson("del2.json", [{ op: "set", sheet: "B", cell: "A1", value: 2 }, { op: "deleteSheet", name: "A" }]), out);
  const xml = parts(out);
  assert.deepEqual(Workbook.open(new Uint8Array(fs.readFileSync(out))).sheetNames(), ["B"]);
  assert.equal(xml["xl/worksheets/sheet1.xml"], undefined);
  assert.doesNotMatch(xml["[Content_Types].xml"], /sheet1\.xml/);
});

test("check reports errors, unsupported functions and circular references", () => {
  const file = path.join(dir, "errors.xlsx");
  cli("create", writeJson("errors.json", { sheets: [{ name: "S", rows: [["=1/0", "=NOSUCHFN(1)", "=C2", "=VLOOKUP(9,A3:B3,2,FALSE)"], [null, null, "=C1"], [1, 2]] }] }), file);
  const result = JSON.parse(cli("check", file));
  const byCell = Object.fromEntries(result.errors.map((entry) => [entry.cell, entry]));
  assert.equal(byCell.A1.error, "#DIV/0!");
  assert.equal(byCell.B1.reason, "function NOSUCHFN is not supported here");
  assert.equal(byCell.D1.error, "#N/A");
  assert.ok(byCell.C1?.reason === "circular reference" || byCell.C2?.reason === "circular reference");
  assert.deepEqual(result.unsupported, ["NOSUCHFN"]);
  const book = Workbook.open(new Uint8Array(fs.readFileSync(file)));
  assert.equal(book.sheet("S").cell("B1").type, "formula");
  assert.equal(book.sheet("S").cell("A1").value, "#DIV/0!");
});

test("a long formula chain evaluates without deep recursion", () => {
  const book = Workbook.blank();
  const sheet = book.addSheet("Chain");
  sheet.set("A1", 1);
  for (let row = 2; row <= 3000; row++) sheet.set(`A${row}`, `=A${row - 1}+1`);
  sheet.set("B1", "=A3000*2");
  const result = recalculate(book);
  assert.deepEqual(result.errors, []);
  assert.equal(sheet.cell("B1").value, 6000);
});

test("bad input fails with a clear message", () => {
  const bad = spawnSync(process.execPath, [CLI, "create", writeJson("bad.json", { sheets: [{ name: "Bad/Name" }] }), path.join(dir, "bad.xlsx")], { encoding: "utf8" });
  assert.equal(bad.status, 1);
  assert.match(bad.stderr, /sheet name has a character Excel refuses/);
  const missing = spawnSync(process.execPath, [CLI, "read", path.join(dir, "nope.xlsx")], { encoding: "utf8" });
  assert.match(missing.stderr, /no such file/);
  const notZip = path.join(dir, "plain.xlsx");
  fs.writeFileSync(notZip, "hello");
  assert.equal(spawnSync(process.execPath, [CLI, "info", notZip], { encoding: "utf8" }).status, 1);
});

test("openpyxl opens the files this skill writes", { skip: spawnSync("python3", ["-c", "import openpyxl"]).status !== 0 && "openpyxl is not installed" }, () => {
  const created = path.join(dir, "created.xlsx");
  const edited = path.join(dir, "foreign-edited.xlsx");
  const script = `
import json, sys, openpyxl
out = {}
wb = openpyxl.load_workbook(sys.argv[1])
ws = wb["Sales"]
out["formula"] = ws["D2"].value
out["bold"] = bool(ws["A1"].font.b)
out["fill"] = ws["A1"].fill.fgColor.rgb
out["freeze"] = ws.freeze_panes
out["width"] = ws.column_dimensions["A"].width
out["numfmt"] = ws["B2"].number_format
vals = openpyxl.load_workbook(sys.argv[1], data_only=True)
out["total"] = vals["Sales"]["D4"].value
out["date"] = vals["Inputs"]["B2"].value.isoformat()
edited = openpyxl.load_workbook(sys.argv[2], data_only=True)
out["edited"] = edited["Summary"]["A1"].value
print(json.dumps(out))
`;
  const result = JSON.parse(execFileSync("python3", ["-c", script, created, edited], { encoding: "utf8" }));
  assert.deepEqual(result, {
    formula: "=SUM(B2:C2)",
    bold: true,
    fill: "FFDDEBF7",
    freeze: "A2",
    width: 22,
    numfmt: "#,##0.00",
    total: 335.5,
    date: "2026-01-31T00:00:00",
    edited: 102,
  });
});
