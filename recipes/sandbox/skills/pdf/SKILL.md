---
type: Skill
name: pdf
description: Read, create and change PDF files. Reads text page by page, and reads scanned pages with OCR. Creates reports with headings, lists, tables, images and page numbers. Fills and flattens forms, merges and splits files, and rotates, deletes, moves, stamps and numbers pages. Use when the user gives you a PDF or asks for one.
version: 1.0.0
author: OpenNeko
license: Apache-2.0
platforms: [linux, macos]
metadata:
  hermes:
    tags: [pdf, documents, forms, merge, ocr]
    category: documents
    requires_toolsets: [terminal]
    related_skills: [docx, xlsx, pptx]
---

# PDF files

One command does each job. It runs in the sandbox with Node:

```bash
node $SKILL_DIR/scripts/pdf.cjs info FILE.pdf [--password P]
node $SKILL_DIR/scripts/pdf.cjs read FILE.pdf [--pages 1-3,7] [--format markdown|text|json] [--layout] [--password P]
node $SKILL_DIR/scripts/pdf.cjs ocr FILE.pdf [--pages 2] [--lang eng]
node $SKILL_DIR/scripts/pdf.cjs render FILE.pdf OUT_DIR [--pages 1] [--dpi 110]
node $SKILL_DIR/scripts/pdf.cjs create SPEC.json OUT.pdf
node $SKILL_DIR/scripts/pdf.cjs edit IN.pdf OPS.json OUT.pdf
node $SKILL_DIR/scripts/pdf.cjs fields FILE.pdf
node $SKILL_DIR/scripts/pdf.cjs fill IN.pdf VALUES.json OUT.pdf [--flatten]
node $SKILL_DIR/scripts/pdf.cjs merge OUT.pdf A.pdf B.pdf:1-3 ... [--bookmarks]
node $SKILL_DIR/scripts/pdf.cjs split IN.pdf OUT_DIR [--every N | --ranges "1-3 4-9"]
```

Use `-` in place of a JSON file to read the JSON from stdin. Page numbers
start at 1. A page list is a number, a range or a list: `3`, `2-5`, `8-`,
`last`, `1,4-6`.

## Read a PDF

1. Run `info` first. It gives the page count, the page sizes, the title and
   author, the bookmarks, the form, any signatures, and the pages that have no
   text layer.
2. Run `read`. The output gives each page under `## Page N`. A long file stops
   at about 60,000 characters, and the output tells you which `--pages` value
   shows the rest.
3. Add `--layout` for tables and forms. It keeps the columns in place with
   spaces, so figures stay under their headings.
4. A page with no text layer is blank, or a scan or a picture. Run `ocr` on
   it. OCR is slow (a few seconds per page), so give `--pages`. For a language
   other than English, give `--lang`, for example `deu` or `eng+fra`.
5. Run `render` when you must see a page, for example a chart or a signature.
   It writes one PNG per page.

An encrypted file needs `--password`. Ask the user for it. Never guess a
password.

## Create a PDF

Write a spec, then run `create`:

```json
{
  "properties": { "title": "Q3 supplier review", "author": "Operations" },
  "page": { "size": "A4", "orientation": "portrait", "margins": 2 },
  "font": { "name": "Helvetica", "size": 11 },
  "header": "Q3 supplier review",
  "footer": "Page {PAGE} of {PAGES}",
  "blocks": [
    { "title": "Q3 supplier review" },
    { "subtitle": "Prepared 04 September" },
    { "heading": "Summary", "level": 1 },
    "Control-module ETA moved from 21 Aug to 29 Aug. It is now eight days late.",
    { "paragraph": [{ "text": "Owner: ", "bold": true }, "Priya Shah"] },
    { "bullets": ["Call the supplier", { "text": "Update the plan", "items": ["Line 2", "Line 4"] }] },
    { "table": [["Supplier", "Days late", "Value"], ["Acme", 8, "$18,400"]], "widths": [8, 4, 5] },
    { "image": "chart.png", "width": 12, "caption": "Figure 1. Late days by week" },
    { "markdown": "## Next steps\n\nAsk **Acme** for a firm date by *Friday*." }
  ]
}
```

`page.size` is `A4`, `Letter`, `Legal`, `A5`, `A3`, `Tabloid`, or
`[width, height]` in cm. Margins, widths and image sizes are in centimetres.
`margins` is one number or `{ "top", "right", "bottom", "left" }`. `header`
and `footer` take text or `{ "text", "align", "size" }`; `{PAGE}` and
`{PAGES}` become page numbers. Headings become bookmarks; set
`"bookmarks": false` to leave them out.

### Blocks

| Block | Fields |
|---|---|
| `"text"` | A paragraph of plain text |
| `paragraph` | Text or runs; `align` (`left`, `center`, `right`), `size`, `bold`, `italic`, `color`, `indent` (cm), `spaceBefore`, `spaceAfter` (points) |
| `title`, `subtitle` | Text or runs |
| `heading` | Text or runs, and `level` 1 to 4 |
| `bullets`, `numbered` | A list of items. An item is text, runs, or `{ "text", "items" }` for a nested list of the same kind. Use `bullets` or `numbered` in place of `items` to change the kind |
| `table` | A list of rows. A cell is text, a number, or `{ "text", "bold", "fill": "#D9E2F3", "align", "colspan" }`. `header` (default `true`) makes row 1 bold and repeats it on each new page. Also `widths`, `align` (one value or one per column), `size`, `stripes`, `caption` |
| `image` | A PNG or JPEG path; `width` or `height` in cm, `align`, `caption` |
| `quote`, `code` | Text |
| `rule` | `true` draws a line |
| `spacer` | A height in cm |
| `pageBreak` | `true` |
| `markdown` | Headings, paragraphs, lists, tables, quotes, code blocks, `**bold**`, `*italic*`, `` `code` `` and links |

A run is `{ "text", "bold", "italic", "underline", "color": "#C00000", "size", "code", "link": "https://..." }`.
Numbers in a table align right unless the cell says otherwise.

### Fonts

The built-in fonts are `Helvetica`, `Times` and `Courier`. They draw Western
European letters, curly quotes, dashes and the euro sign. For other
characters, such as ₹, →, Greek, Cyrillic or Chinese, give a TrueType or
OpenType font: `"font": { "name": "fonts/NotoSans-Regular.ttf" }`, or
`{ "regular": ..., "bold": ..., "italic": ..., "boldItalic": ... }`. If no
font is at hand, ask the user for one. The tool stops and names the first
character that the font cannot draw.

## Fill a form

1. Run `fields`. It lists each field with its name, type, value, options,
   limits and page.
2. Write the values as one JSON object, field name to value:

```json
{ "supplier.name": "Acme Ltd", "approved": true, "tier": "silver", "region": "APAC", "tags": ["a", "c"] }
```

3. Run `fill`. Add `--flatten` when the user wants a final copy that nobody
   can change. Flatten draws the values into the page and removes the fields.

| Type | Value |
|---|---|
| `text` | Text or a number; `null` clears it |
| `checkbox` | `true` or `false` |
| `radio` | One of the listed options; `null` clears it |
| `dropdown`, `list` | One option, or a list of options when the field allows more than one |

Fill refuses an unknown field name, a value that is not an option, text over
the field's limit and a read-only field. Read the message, then correct the
value. Change a read-only field only when the user asks: write
`{ "values": { ... }, "force": true }`. For characters outside the built-in
fonts, add `--font FONT.ttf`. A form that is XFA only cannot be filled. A form
with both an XFA and a normal copy is filled, and the XFA copy is removed so
that every viewer shows the values.

## Merge and split

`merge OUT.pdf A.pdf B.pdf:2-4 C.pdf` joins the files in order. Add
`:PAGES` to a file to take only those pages. Form fields stay live. When two
files use the same field name, the second field gets a new name, such as
`code_2`, and the output lists each new name. Add `--bookmarks` to make one
bookmark per input file.

`split IN.pdf OUT_DIR` writes one file per page. `--every 5` writes five
pages per file. `--ranges "1-3 4-9 10-"` writes one file per range.

## Edit pages

`edit` takes a list of operations, or `{ "ops": [...] }`. The ops run in
order, and page numbers in each op refer to the file as the ops before it left
it. `pages` defaults to all pages.

| Operation | Fields |
|---|---|
| `rotate` | `pages`, `degrees` (90, 180, 270 or -90) |
| `delete` | `pages` |
| `keep` | `pages`; deletes every other page |
| `reorder` | `order`: every page once, for example `"3,1-2,4-"` |
| `move` | `pages`, `to` (a position, `start` or `end`) |
| `insert` | `file` and `pages`, or `"blank": true` with `size` and `count`; and `after` (a page number, 0 for the start) or `"at": "start"`. Default: at the end |
| `text` | `pages`, `text`, and `x` and `y` in cm from the top-left corner, or `position` (`top-left`, `top-center`, `top-right`, `bottom-left`, `bottom-center`, `bottom-right`) with `margin`; `size`, `color`, `bold`, `italic`, `font`, `align`, `opacity`, `background` |
| `image` | `pages`, `image` (PNG or JPEG), `x` and `y` or `position`, `width` or `height` in cm, `opacity` |
| `watermark` | `text`, `pages`, `color`, `opacity` (default 0.15), `angle` (default 45), `size` |
| `pageNumbers` | `format` (default `{PAGE} / {PAGES}`), `pages`, `position` (default `bottom-center`), `start`, `size` |
| `crop` | `pages`, and `top`, `right`, `bottom`, `left` in cm |
| `resize` | `pages`, `size`, `orientation`; scales the content to fit |
| `properties` | `title`, `author`, `subject`, `keywords`, `creator`, `language` |
| `fill` | `values`, as in `fill` |
| `flatten` | Draws the form values into the pages and removes the fields |

To change the words in a PDF, read it, then create a new file. A PDF holds
placed text, so this skill does not edit text in place.

## Safety

- **Redaction.** This skill cannot redact. A box drawn on a page leaves the
  text under it in the file, and anyone can copy it out. When the user asks
  for redaction, say so and point them to a redaction tool.
- **Signatures.** An edit of a signed file makes each digital signature
  invalid. The tool refuses a signed file. Tell the user, and add
  `--break-signatures` only when they agree.
- **Encryption.** The tool reads an encrypted file with `--password`, but it
  cannot change one. Ask the user for a copy without a password.

## Limits

- No tagged (accessible) PDF output, no PDF/A, and no encryption.
- No text edits in place, no annotation edits other than form fields, and no
  digital signing.
- `read` gives text in the order that the file stores it. Most files store
  it in reading order. If a page reads out of order, use `--layout`.
- OCR quality depends on the scan. Check figures from OCR against the page
  image from `render` before you use them.
- The tool stops on a table row that is taller than a page.
