---
type: Skill
name: docx
description: Create, read and edit Word documents (.docx, .docm) with headings, lists, tables, images, links, headers and footers, and make edits as tracked changes with comments that a reviewer can accept or reject. Use when the user gives you a Word document or asks for one.
version: 1.0.0
author: OpenNeko
license: Apache-2.0
platforms: [linux, macos]
metadata:
  hermes:
    tags: [word, docx, document, tracked-changes, redline]
    category: documents
    requires_toolsets: [terminal]
    related_skills: [xlsx, document-extraction]
---

# Word documents

One command does each job. It runs in the sandbox with Node:

```bash
node $SKILL_DIR/scripts/docx.cjs info FILE.docx
node $SKILL_DIR/scripts/docx.cjs read FILE.docx [--format markdown|text|json] [--changes show|accept|reject] [--numbers] [--comments] [--from N] [--to N]
node $SKILL_DIR/scripts/docx.cjs changes FILE.docx
node $SKILL_DIR/scripts/docx.cjs comments FILE.docx
node $SKILL_DIR/scripts/docx.cjs create SPEC.json OUT.docx
node $SKILL_DIR/scripts/docx.cjs edit IN.docx OPS.json OUT.docx [--track] [--author NAME]
```

Use `-` in place of a JSON file to read the JSON from stdin.

## Read a document

1. Run `info` first. It gives the title, the word count, the headings with
   their paragraph numbers, the tables, the page setup, the comment count and
   the tracked changes by author.
2. Run `read`. The default output is Markdown: headings, lists, tables, bold,
   italic and links. Footnote marks show as `[^1]` and images as
   `![description]`.
3. Add `--numbers` before you edit. Each paragraph then starts with its
   number, for example `[12]`. Paragraphs in table cells have numbers too.
4. A long document stops at about 60,000 characters. The output tells you
   which `--from` value shows the rest.

Tracked changes show as `{++inserted text++}` and `{--deleted text--}`. Use
`--changes accept` to read the text as it is after all changes are accepted,
or `--changes reject` to read the original. `changes` lists each change with
its id, type, author, date and paragraph. `--comments` adds the comments at
the end; `comments` lists them as JSON with the text that each one marks.

## Create a document

Write a spec, then run `create`:

```json
{
  "properties": { "title": "Q3 supplier review", "author": "Operations" },
  "page": { "size": "A4", "orientation": "portrait", "margins": 2.5 },
  "font": { "name": "Calibri", "size": 11 },
  "header": "Q3 supplier review",
  "footer": "Page {PAGE} of {PAGES}",
  "blocks": [
    { "title": "Q3 supplier review" },
    { "heading": "Summary", "level": 1 },
    "Control-module ETA moved from 21 Aug to 29 Aug. It is now eight days late.",
    { "paragraph": [{ "text": "Owner: ", "bold": true }, "Priya Shah"] },
    { "bullets": ["Call the supplier", { "text": "Update the plan", "items": ["Line 2", "Line 4"] }] },
    { "table": [["Supplier", "Days late", "Value"], ["Acme", 8, "$18,400"]], "widths": [8, 4, 4] },
    { "markdown": "## Next steps\n\nAsk **Acme** for a firm date by *Friday*." }
  ]
}
```

`page.size` is `A4`, `Letter`, `Legal`, `A5` or `A3`. Margins and widths are
in centimetres. `header` and `footer` take text or `{ "text": ..., "align": ... }`;
`{PAGE}` and `{PAGES}` become page numbers.

### Blocks

| Block | Fields |
|---|---|
| `"text"` | A paragraph of plain text |
| `paragraph` | Text or runs; `style`, `align` (`left`, `center`, `right`, `justify`), `indent` (cm), `spaceBefore`, `spaceAfter` (points), `lineSpacing` (1.15 = 115%), `keepWithNext`, `pageBreakBefore` |
| `heading` | Text or runs, and `level` 1 to 6 |
| `title`, `subtitle`, `quote` | Text or runs |
| `bullets`, `numbered` | A list of items. An item is text, runs, or `{ "text": ..., "items": [...] }` for a nested list. Each list starts at 1 |
| `table` | A list of rows. A cell is text, runs, or `{ "text", "bold", "fill": "#D9E2F3", "align", "colspan" }`. `header` (default `true`) makes row 1 bold and repeats it on each page. Also `widths`, `caption`, `style` |
| `image` | A PNG, JPEG or GIF path; `width` or `height` in cm, `alt`, `caption`, `align` |
| `pageBreak` | `true` |
| `toc` | `true`; `depth` (default 3) and `title` (default `Contents`). Word fills the table of contents when it opens the file |
| `markdown` | Markdown text: headings, paragraphs, lists, tables, quotes, code blocks, `**bold**`, `*italic*`, `` `code` ``, `~~strike~~` and links |

A run is `{ "text": ..., "bold", "italic", "underline", "strike", "color": "#C00000", "highlight": "yellow", "size": 12, "font": "Arial", "superscript", "subscript", "code", "link": "https://..." }`.
A plain string in `paragraph` or a cell is literal text. Use a `markdown`
block when you want Markdown formatting.

## Edit a document

`edit` takes a list of operations, or `{ "track": true, "author": "Name", "ops": [...] }`:

| Operation | Fields |
|---|---|
| `replace` | `find`, `with`; `all` or `occurrence` when the text occurs more than once; `"scope": "all"` to include headers, footers and notes |
| `insert` | `blocks`, `markdown` or `text`; and `after` or `before` (an anchor), or `at` (`start` or `end`) |
| `delete` | An anchor (`paragraph`, `find` or `heading`) and `count` paragraphs, or `table` (a table number) |
| `format` | `find`, and `bold`, `italic`, `underline`, `strike`, `color`, `highlight`, `size` or `font` (`false` removes it) |
| `style` | An anchor, and `style` (a style name), `heading` (a level) or `align` |
| `comment` | `find`, `text`; `author` and `initials` |
| `cell` | `table`, `row`, `column` (each from 1), `text` |
| `addRow` | `table`, `cells`; `after` (a row number; default the last row). The new row copies the format of the row above |
| `deleteRow` | `table`, `row` |
| `accept`, `reject` | `author` or `ids` to pick changes; no field means all changes |
| `properties` | `title`, `subject`, `author`, `keywords`, `description` |
| `trackChanges` | `on` (default `true`) turns on change tracking in Word for the next person |

An anchor is a paragraph number from `read --numbers`, a piece of text in the
paragraph, or an object: `{ "paragraph": 12 }`, `{ "find": "text", "occurrence": 2 }`,
`{ "heading": "Summary" }` or `{ "table": 1 }`. Paragraph numbers change after an
insert or a delete, so an op that comes later in the list sees the new numbers.
Text anchors do not change.

`find` matches text across bold, italic and other format changes inside a
paragraph. Straight and curly quotes match each other. The new text takes the
format of the first character that it replaces. A `find` that occurs more than
once is refused until you add `all`, `occurrence` or more text.

Edit keeps the parts of the file that it does not change: styles, numbering,
footnotes, fields, content controls, custom properties and other tracked changes.

## Tracked changes and comments

Add `--track` (or `"track": true`) when the user wants to review the edits, for
example in a contract or a policy. Each edit then shows in Word as a tracked
insertion, deletion, format change, paragraph change or table row change, with
the author and the date. A single op can set its own `"track"` and `"author"`.

- Give `--author` the name that the user wants to see in Word. The default is
  `OpenNeko`.
- Use `comment` to explain an edit or to ask a question.
- Use `accept` or `reject` with `author` to resolve one reviewer's changes.
- You can change your own tracked insertion again. Another author's tracked
  insertion is refused until it is accepted or rejected.
- Run `read` after a tracked edit and look at each `{++ ++}` and `{-- --}`
  before you give the file to the user.

## Good practice

- Read the document before you edit it, and copy `find` text from that output.
- Use `replace` for words and sentences and `insert` with `delete` for whole
  paragraphs.
- Use heading blocks for headings, so that the outline and the table of
  contents work.
- Save a file that the user should get in the run's artifact directory.

## Limits

- `.doc`, `.rtf`, `.odt` and Word templates (`.dotx`) are not supported. Ask
  for a `.docx`, or use the `document-extraction` skill to read their text.
- A file that unpacks to more than 512 MB is refused.
- `find` does not cross paragraphs, links, fields or tracked changes. Text in
  a field (a page number, a date field, a table of contents) is refused; the
  user changes fields in Word.
- The tool does not change text in text boxes or shapes, and it does not make
  charts, footnotes or equations. It keeps the ones that a file holds.
- Images go in the body only, not in a header or footer.
