# Library files

The Library holds each user's uploaded documents and spreadsheets. OpenNeko
stores them in the user's private folder of `openneko serve`.

## Accepted inputs

PDF (with a text layer), DOCX, PPTX, XLSX, CSV, Markdown and plain text
(`formats.ts`). Scanned documents are stored, but only their file name is
searchable until OCR exists.

## Storage

`store.ts` writes an upload through the files client:

```text
orgs/<org id>/users/<user id>/library/<sha256>/<filename>
```

`library_document.relative_path` stores `files/library/<sha256>/<filename>`.
The upload is content-addressed, so a later upload with the same name never
overwrites an earlier one. Removing a document deletes its row and its file.

## Reading

The agent reads the Library with the `neko_files` tools: `search`,
`read_text` and `query_tables`. The broker takes the user from the run owner,
so a run sees only its owner's folder. Nothing is extracted, chunked or
summarised on the Node side.
