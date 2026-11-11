---
type: Skill
name: xlsx
description: Create, read and edit Excel workbooks (.xlsx, .xlsm) with values, formulas, styles, dates, frozen panes, merged cells and defined names, and recalculate the formulas to find errors. Use when the user gives you a spreadsheet or asks for one.
version: 1.0.0
author: OpenNeko
license: Apache-2.0
platforms: [linux, macos]
metadata:
  hermes:
    tags: [spreadsheet, excel, xlsx, formulas]
    category: documents
    requires_toolsets: [terminal]
    related_skills: [document-extraction]
---

# Excel workbooks

One command does each job. It runs in the sandbox with Node:

```bash
node $SKILL_DIR/scripts/xlsx.cjs info FILE.xlsx
node $SKILL_DIR/scripts/xlsx.cjs read FILE.xlsx [--sheet NAME] [--range A1:F40] [--format markdown|csv|json] [--formulas] [--max-rows N] [--all-sheets]
node $SKILL_DIR/scripts/xlsx.cjs create SPEC.json OUT.xlsx
node $SKILL_DIR/scripts/xlsx.cjs edit IN.xlsx OPS.json OUT.xlsx
node $SKILL_DIR/scripts/xlsx.cjs recalc IN.xlsx [OUT.xlsx]
node $SKILL_DIR/scripts/xlsx.cjs check FILE.xlsx
```

Use `-` in place of a JSON file to read the JSON from stdin.

## Read a workbook

1. Run `info` first. It lists the sheets, the used range of each sheet, the
   formula count and the defined names.
2. Run `read` for the part you need. The default output is a Markdown table
   with column letters and row numbers, and it stops at 200 rows. Use
   `--range` or `--max-rows` to see more.
3. Add `--formulas` to see each formula in place of its value. With
   `--format json --formulas`, you get the values and a map of formulas.

Dates come back as `YYYY-MM-DD` (or `YYYY-MM-DDTHH:MM:SS`) when the cell has a
date format. The values of formulas come from the values that the file holds.
Run `check` if you are not sure that they are current.

## Create a workbook

Write a spec, then run `create`:

```json
{
  "sheets": [
    {
      "name": "Sales",
      "rows": [
        ["Region", "Q1", "Q2", "Total"],
        ["North", 120, 135, "=SUM(B2:C2)"],
        ["South", 80, 95, "=SUM(B3:C3)"],
        ["Total", "=SUM(B2:B3)", "=SUM(C2:C3)", "=SUM(D2:D3)"]
      ],
      "columns": { "A": 18 },
      "freeze": "A2",
      "styles": [
        { "range": "A1:D1", "style": { "bold": true, "fill": "#DDEBF7", "border": "thin" } },
        { "range": "B2:D4", "style": { "numFmt": "#,##0" } }
      ]
    },
    { "name": "Inputs", "rows": [["Growth", 0.05], ["Start", { "date": "2026-01-31" }]] }
  ],
  "names": [{ "name": "Growth", "ref": "Inputs!$B$1" }],
  "ops": []
}
```

A sheet can also have `start` (the top-left cell of `rows`, default `A1`) and
`merge` (a list of ranges). `ops` takes the edit operations below and runs
after the sheets are made.

### Cell values

| Write | Result |
|---|---|
| `120`, `0.05` | A number |
| `"North"` | Text |
| `true` | A boolean |
| `null` | An empty cell |
| `"=SUM(B2:C2)"` | A formula |
| `{ "date": "2026-01-31" }` | A date, with a `yyyy-mm-dd` format unless you give one |
| `{ "text": "=not a formula" }` | Text that starts with `=` |
| `{ "value": 120, "style": { "bold": true } }` | A value with a style |
| `{ "formula": "B2*Growth" }` | A formula, with or without `=` |

Write formulas the way you type them in Excel, in English, with commas between
arguments. The tool adds the `_xlfn.` prefix that newer functions such as
`XLOOKUP`, `IFS`, `TEXTJOIN` and `STDEV.S` need in the file.

### Styles

`numFmt` (an Excel format code, for example `"#,##0.00"`, `"0.0%"`,
`"yyyy-mm-dd"`), `bold`, `italic`, `underline`, `size`, `font` (a font name),
`color` and `fill` (`#RRGGBB`), `align` (`left`, `center`, `right`),
`valign` (`top`, `center`, `bottom`), `wrap` (`true` or `false`) and
`border` (`thin`, `medium`, `thick`, `dashed`, `dotted`, `double`, `hair` or
`none`). A new style adds to the style that the cell has.

## Edit a workbook

`edit` takes a list of operations (or `{ "ops": [...] }`) and applies them in
order:

| Operation | Fields |
|---|---|
| `set` | `sheet`, `cell`, and `value`, `formula`, `date`, `text` or `style` |
| `rows` | `sheet`, `start`, `rows` (a block of values, as in a spec) |
| `append` | `sheet`, `rows`; writes from column A below the last used row |
| `style` | `sheet`, `range`, `style` |
| `addSheet` | `name` |
| `renameSheet` | `from`, `to`; formulas and names that use the sheet change too |
| `deleteSheet` | `name`; refused while a formula or name on another sheet uses it |
| `columns` | `sheet`, `widths` (for example `{ "A": 20, "B": 12 }`) |
| `freeze` | `sheet`, `cell` (`"A2"` freezes row 1; `"A1"` removes the freeze) |
| `merge` | `sheet`, `range` |
| `name` | `name`, `ref`, and `scope` (a sheet name) for a sheet-level name |

`sheet` is not case-sensitive. Leave it out to use the first sheet.

Edit keeps the parts of the file that the tool does not change: charts,
images, conditional formats, data validation, comments, other sheets and
document properties.

The tool cannot insert or delete rows or columns, because formula references
would have to move. To change the layout, read the data, then write a new
block with `rows`, or create a new workbook.

## Formulas and recalculation

`create`, `edit` and `recalc` calculate every formula, store the results in the
file and print a summary:

```json
{ "formulas": 12, "errors": [{ "sheet": "Sales", "cell": "D7", "formula": "=B7/C7", "error": "#DIV/0!" }], "unsupported": [] }
```

- Fix each entry in `errors` before you give the file to the user, unless the
  error is what the user wants.
- `unsupported` lists functions that this tool cannot calculate. Such a cell
  has no stored value. Excel and LibreOffice calculate it when the file opens,
  because the tool tells them to recalculate on open. Tell the user which
  cells those are.
- `check` calculates and reports without a change to the file.
- `--no-recalc` on `create` or `edit` skips the calculation.

The tool calculates about 400 functions. It does not calculate dynamic array
functions (`FILTER`, `SORT`, `UNIQUE`, `SEQUENCE`), links to other workbooks,
or array formulas. Do not use dynamic array functions; use `SUMIFS`,
`COUNTIFS`, `INDEX` and `MATCH` or `XLOOKUP` instead.

## Good practice

- Put inputs (rates, dates, targets) in their own cells or on an `Inputs`
  sheet. Refer to them from formulas, and give the important ones a name.
- Use formulas for totals and ratios that the user can audit. Write a plain
  value only when the number comes from outside the workbook.
- Give every money and percent column a number format.
- Freeze the header row of a long table.
- After `create` or `edit`, run `read` on the result and look at the numbers
  before you give the file to the user.
- Save a file that the user should get in the run's artifact directory.

## Limits

- `.xls`, `.xlsb` and `.ods` files are not supported. Ask for `.xlsx`, or use
  the `document-extraction` skill to read their text.
- A file that unpacks to more than 512 MB is refused.
- The tool does not make charts or pivot tables. It keeps the ones that a file
  already holds, but their cached data does not change until Excel refreshes
  them.
