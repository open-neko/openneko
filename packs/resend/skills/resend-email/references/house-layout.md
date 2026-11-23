# House email layout

OpenNeko cleans the `html` body before the approver sees the email. The
recipient gets the structure of the email: its text, headings, lists, tables
and links. Write the layout with that structure only.

## What OpenNeko keeps

- **Tags:** `p`, `br`, `div`, `span`, `h1` to `h6`, `strong`, `b`, `em`, `i`,
  `u`, `ul`, `ol`, `li`, `a`, `table`, `thead`, `tbody`, `tfoot`, `tr`, `th`,
  `td`, `code`, `pre`, `blockquote`, `hr` and `small`.
- **Attributes:** `href` on a link, and `colspan` and `rowspan` on a cell.
- **Links:** `https:`, `http:` and `mailto:` only. When the link text does not
  name the link's host, OpenNeko adds the host after the link.

OpenNeko removes every other attribute, including `style`, `class` and `width`.
It removes the document head, images and anything that loads or runs content.
A logo, a brand color or an inline style never reaches the recipient.

## Layout

Put the parts in this order:

1. A heading with the subject of the email.
2. The content: paragraphs, lists and tables.
3. A footer: `<hr>`, then one line in `<small>`.

```html
<h2>{{SUBJECT}}</h2>
{{CONTENT}}
<hr>
<p><small>{{FOOTER_TEXT}}</small></p>
```

## Content elements

| Element | Markup |
|---|---|
| Heading | `<h3>…</h3>` |
| Paragraph | `<p>…</p>` |
| Link | `<a href="https://example.com/report">example.com report</a>` |
| List | `<ul><li>…</li></ul>` |
| Table | `<table><thead><tr><th>…</th></tr></thead><tbody><tr><td>…</td></tr></tbody></table>` |
| Code | `<code>…</code>` |

## Safety rules

- Escape every value that comes from data: `&`, `<`, `>`, `"` and `'`.
- Name the host in the link text, so the reader sees where the link goes.
- The `text` body carries the same content as plain text, with the footer
  after a `--` line.
