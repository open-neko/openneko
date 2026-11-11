---
type: Skill
name: pptx
description: Create, read and edit PowerPoint decks (.pptx, .pptm, .potx) with titles, bullets, tables, charts, pictures and speaker notes, build a deck from a company template's layouts, and fill or change slides in an existing deck. Use when the user gives you a presentation or asks for one.
version: 1.0.0
author: OpenNeko
license: Apache-2.0
platforms: [linux, macos]
metadata:
  hermes:
    tags: [powerpoint, pptx, slides, presentation, deck, template]
    category: documents
    requires_toolsets: [terminal]
    related_skills: [xlsx, docx]
---

# PowerPoint decks

One command does each job. It runs in the sandbox with Node:

```bash
node $SKILL_DIR/scripts/pptx.cjs info FILE.pptx
node $SKILL_DIR/scripts/pptx.cjs layouts FILE.pptx
node $SKILL_DIR/scripts/pptx.cjs read FILE.pptx [--format markdown|text|json] [--slides 2-4,7] [--ids] [--no-notes]
node $SKILL_DIR/scripts/pptx.cjs create SPEC.json OUT.pptx
node $SKILL_DIR/scripts/pptx.cjs edit IN.pptx OPS.json OUT.pptx
```

Use `-` in place of a JSON file to read the JSON from stdin.

## Read a deck

1. Run `info` first. It gives the slide size, the properties, each slide's
   title, layout and contents, and the names of the layouts.
2. Run `read`. The default output is Markdown: one `## Slide N` section per
   slide, with bullets, tables, chart data, pictures and speaker notes.
3. Add `--ids` before you edit. Each shape then starts with its id, its name
   and its placeholder, for example `[#3 Content Placeholder 2, obj 1]`.
4. A long deck stops at about 60,000 characters. The output tells you which
   `--slides` value shows the rest.

`--format json` gives each shape's id, name, kind, placeholder, position in
centimetres, paragraphs with levels, table cells and chart series.

## Create a deck

Write a spec, then run `create`:

```json
{
  "properties": { "title": "Q3 supplier review", "author": "Operations" },
  "size": "16:9",
  "footer": "Q3 supplier review",
  "slideNumbers": true,
  "slides": [
    { "title": "Q3 supplier review", "subtitle": "Operations · 04 September" },
    { "title": "Summary", "body": ["Control-module ETA moved from 21 Aug to 29 Aug", { "text": "It is now eight days late", "items": ["Line 2", "Line 4"] }], "notes": "Give the new date first." },
    { "title": "Days late", "body": { "table": [["Supplier", "Days late"], ["Acme", 8], ["Bolt Co", 3]], "widths": [3, 1] } },
    { "title": "Trend", "body": { "chart": "column", "categories": ["Jul", "Aug", "Sep"], "series": [{ "name": "Late", "values": [2, 5, 8] }] } },
    { "title": "Two suppliers", "left": ["Acme", "Bolt Co"], "right": { "image": "logo.png", "alt": "Acme logo" } },
    { "section": "Next steps" }
  ]
}
```

`size` is `16:9` (the default), `4:3`, `16:10` or `{ "w": 20, "h": 10 }` in
centimetres. `footer` and `slideNumbers` go on every slide whose layout has a
footer or a slide number placeholder.

### Slides

The tool picks a layout from the fields of a slide: `subtitle` gives a title
slide, `left` and `right` give two columns, `section` gives a section header,
`body` gives title and content, a `title` alone gives title only, and no
title gives a blank slide. Set `layout` to a layout name or number from
`layouts` to choose one yourself.

| Field | What it holds |
|---|---|
| `title`, `subtitle`, `section` | Text |
| `body`, `left`, `right` | Content for the layout's content placeholders |
| `placeholders` | `{ "13": content, "Logo": content }`: content for a placeholder by its index or its name from `layouts` |
| `shapes` | A list of shapes at a position that you give |
| `notes` | Speaker notes: text or paragraphs |
| `background` | `#RRGGBB` |
| `hidden` | `true` hides the slide in the slide show |
| `layout` | A layout name or number |
| `footer`, `slideNumber` | Override the deck values for this slide |

Content is one of:

- Text: a string, or a list of paragraphs. A line break in a string starts
  a new paragraph. A paragraph is a string, a list of runs, or
  `{ "text": ..., "level": 1, "items": [...] }`; `items` holds the next level.
- `{ "text": [...], "bullets": false, "size": 20, "align": "center", "bold": true, "color": "#C00000" }`
  to format all of the text. `bullets` is `true`, `false` or `"number"`.
- `{ "table": rows }`: a cell is text, a number, or
  `{ "text", "bold", "fill": "#FDE68A", "align", "colspan" }`. Also `widths`
  (relative), `header` (default `true`), `banded`, `size`.
- `{ "chart": "column", "categories": [...], "series": [{ "name", "values", "color" }] }`.
  The kinds are `column`, `bar`, `stackedColumn`, `stackedBar`, `line`, `pie`
  and `doughnut`. Also `title`, `labels`, `percent` (pie), `legend`
  (`true`, `false`, `bottom`, `top`, `left`, `right`), `numberFormat` (an
  Excel format such as `"$#,##0"`) and `axis: false`. PowerPoint opens the
  chart data in Excel.
- `{ "image": "file.png" }`: PNG, JPEG or GIF. `fit` is `fill` (default in
  a placeholder; it crops), `fit` (it keeps the whole image) or `stretch`.
  Add `alt` for a screen reader.

A run is `{ "text": ..., "bold", "italic", "underline", "strike", "color", "size", "font", "superscript", "subscript", "link": "https://..." }`.

### Shapes

A shape has `x`, `y`, `w` and `h` in centimetres from the top left corner,
and one of `text`, `image`, `table` or `chart` as above. A text shape can
also have `shape` (`rect`, `rounded`, `ellipse`, `triangle`, `arrow`,
`chevron`), `fill`, `line` (`"#RRGGBB"` or `{ "color", "width" }` in points),
`valign` (`top`, `middle`, `bottom`), `margin` (cm), `autofit` (`shrink` or
`resize`), `name` and `alt`. An image needs only `x`, `y` and `w` or `h`;
the tool keeps its proportions.

## Use a template

A company template sets the fonts, colours, logos and layouts. Use it when
the user gives you one:

1. Run `layouts TEMPLATE.potx`. It lists each layout with the index, type,
   name and position of each placeholder.
2. Write a spec with `"template": "TEMPLATE.potx"`. Each slide names its
   `layout` and fills `title`, `body` or `placeholders` by index or name.
3. The new deck keeps the template's masters, layouts, theme and other
   parts, and drops the template's own slides. Add `"keepSlides": true` to
   keep them, for example to add slides to an existing deck.

A `.pptx` works as a template too. Save the output as `.potx` to make a
template.

To fill a deck that holds markers such as `{{client}}`, run `edit` with
`replace` and `"all": true`.

## Edit a deck

`edit` takes a list of operations, or `{ "ops": [...] }`. Slides and
positions count from 1. An op sees the slide numbers that the ops before it
leave.

| Operation | Fields |
|---|---|
| `addSlide` | The slide fields above, and `after` (a slide number) or `position` |
| `duplicate` | `slide`; `position` (default: after the slide). The copy gets its own charts and notes |
| `delete` | `slide`: a number or a list |
| `move` | `slide`, `to` |
| `replace` | `find`, `with`; `slide` (a number or a list) to limit it; `all` or `occurrence` when the text occurs more than once; `notes: true` to include speaker notes |
| `setText` | `slide`, `shape`, `text` (content as above). The new text keeps the format of the first run |
| `addShape` | `slide` and the shape fields above |
| `deleteShape` | `slide`, `shape` |
| `moveShape` | `slide`, `shape`, and any of `x`, `y`, `w`, `h` |
| `image` | `slide`, `shape` (a picture), `image`; `fit`, `alt` |
| `cell` | `slide`, `row`, `column`, `text`; `shape` when the slide has more than one table |
| `addRow` | `slide`, `cells`; `after` (a row number). The row copies the format of the row above |
| `deleteRow` | `slide`, `row` |
| `chart` | `slide`, `categories`, `series`; `title`; `shape` when the slide has more than one chart. The chart keeps its kind and style |
| `notes` | `slide`, `text` (`""` removes the notes) |
| `background` | `color` (`null` removes it); `slide` (default all) |
| `hide`, `show` | `slide` (a number or a list) |
| `properties` | `title`, `subject`, `author`, `keywords`, `description` |

`shape` is the id from `read --ids`, the shape's name, or `title`,
`subtitle` or `body`. `setText` with `title`, `subtitle` or `body` fills the
layout's placeholder when the slide does not have one yet.

`find` matches text across bold, italic and other format changes inside a
paragraph. Straight and curly quotes match each other. The new text takes
the format of the first character that it replaces. A `find` that occurs
more than once is refused until you add `all`, `occurrence`, `slide` or more
text.

Edit keeps the parts of the file that it does not change: masters, layouts,
themes, sections, animations, comments, custom properties and other slides.
Delete removes the files that only the deleted slide or shape used. When an
animation uses a shape that you delete, the slide loses its animations, and
the output says so.

## Good practice

- Put one idea on a slide, as a title that states the point and three to
  five bullets.
- Use the layout placeholders for titles and body text. Then the template
  controls the fonts and positions, and the outline view works.
- Put the detail in speaker notes, not on the slide.
- Read the deck before you edit it, and copy `find` text from that output.
- Run `read` on the output and check each changed slide before you give the
  file to the user.
- Save a file that the user should get in the run's artifact directory.

## Limits

- `.ppt`, `.pps`, `.odp` and Keynote files are not supported. Ask for a
  `.pptx`.
- A file that unpacks to more than 512 MB is refused.
- The tool does not render slides to images, so it cannot check how a slide
  looks. Keep text short, so that it fits its placeholder.
- `find` does not cross paragraphs or line breaks, and it does not search
  chart titles or SmartArt.
- The tool does not make animations, transitions, SmartArt, video or audio.
  It keeps the ones that a file holds.
- `chart` changes a chart with one plot. It does not change scatter or
  bubble charts.
- A deck saved as `.pptx` loses its macros; the output says so. Save as
  `.pptm` to keep them.
