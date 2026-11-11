"use strict";

const {
  PDFArray,
  PDFButton,
  PDFCheckBox,
  PDFDict,
  PDFDropdown,
  PDFName,
  PDFObjectCopier,
  PDFOptionList,
  PDFRadioGroup,
  PDFSignature,
  PDFString,
  PDFHexString,
  PDFTextField,
} = require("pdf-lib");
const { fail, pageRefIndex, toCm } = require("./common.cjs");

function fieldType(field) {
  if (field instanceof PDFTextField) return "text";
  if (field instanceof PDFCheckBox) return "checkbox";
  if (field instanceof PDFRadioGroup) return "radio";
  if (field instanceof PDFDropdown) return "dropdown";
  if (field instanceof PDFOptionList) return "list";
  if (field instanceof PDFSignature) return "signature";
  if (field instanceof PDFButton) return "button";
  return "unknown";
}

function widgetPages(doc, field) {
  const index = pageRefIndex(doc);
  const byWidget = new Map();
  doc.getPages().forEach((page, i) => {
    const annots = page.node.Annots();
    if (!annots) return;
    for (let k = 0; k < annots.size(); k++) byWidget.set(annots.get(k).toString(), i);
  });
  const out = [];
  for (const widget of field.acroField.getWidgets()) {
    let pageIndex;
    const pageRef = widget.P();
    if (pageRef && index.has(pageRef.toString())) pageIndex = index.get(pageRef.toString());
    else {
      const ref = doc.context.getObjectRef(widget.dict);
      if (ref && byWidget.has(ref.toString())) pageIndex = byWidget.get(ref.toString());
    }
    const r = widget.getRectangle();
    out.push({ page: pageIndex === undefined ? null : pageIndex + 1, rect: [toCm(r.x), toCm(r.y), toCm(r.width), toCm(r.height)] });
  }
  return out;
}

function describe(doc, field) {
  const type = fieldType(field);
  const entry = { name: field.getName(), type };
  try {
    if (type === "text") {
      entry.value = field.getText() ?? "";
      if (field.isMultiline()) entry.multiline = true;
      const max = field.getMaxLength();
      if (max !== undefined) entry.maxLength = max;
    } else if (type === "checkbox") {
      entry.value = field.isChecked();
    } else if (type === "radio") {
      entry.value = field.getSelected() ?? null;
      entry.options = field.getOptions();
    } else if (type === "dropdown") {
      const selected = field.getSelected();
      entry.value = field.isMultiselect() ? selected : selected[0] ?? null;
      entry.options = field.getOptions();
      if (field.isEditable()) entry.editable = true;
    } else if (type === "list") {
      const selected = field.getSelected();
      entry.value = field.isMultiselect() ? selected : selected[0] ?? null;
      entry.options = field.getOptions();
    }
  } catch (error) {
    entry.problem = error.message;
  }
  if (field.isRequired()) entry.required = true;
  if (field.isReadOnly()) entry.readOnly = true;
  const tip = field.acroField.dict.lookupMaybe(PDFName.of("TU"), PDFString, PDFHexString);
  if (tip) entry.label = tip.decodeText();
  entry.widgets = widgetPages(doc, field);
  return entry;
}

function listFields(doc) {
  const form = doc.getForm();
  return { xfa: form.hasXFA(), fields: form.getFields().map((field) => describe(doc, field)) };
}

function closest(name, names) {
  const lower = name.toLowerCase();
  return names.filter((n) => n.toLowerCase().includes(lower) || lower.includes(n.toLowerCase())).slice(0, 5);
}

function fill(doc, values, options = {}) {
  if (!values || typeof values !== "object" || Array.isArray(values)) fail("values must be a JSON object of field name to value");
  const form = doc.getForm();
  const fields = form.getFields();
  if (!fields.length) fail(form.hasXFA() ? "the form is XFA only; this skill fills AcroForm fields and cannot fill it" : "the file has no form fields");
  const byName = new Map(fields.map((f) => [f.getName(), f]));
  const filled = [];
  for (const [name, value] of Object.entries(values)) {
    const field = byName.get(name);
    if (!field) {
      const near = closest(name, [...byName.keys()]);
      fail(`no field named "${name}"${near.length ? `; close names: ${near.join(", ")}` : ""}. Run fields to list them`);
    }
    if (field.isReadOnly() && !options.force) fail(`field "${name}" is read-only; add "force": true to the values file only when the user asks`);
    const type = fieldType(field);
    const want = (ok, msg) => ok || fail(`field "${name}" (${type}) ${msg}`);
    if (type === "text") {
      want(["string", "number"].includes(typeof value) || value === null, "takes text or a number");
      const text = value === null ? "" : String(value);
      const max = field.getMaxLength();
      want(max === undefined || text.length <= max, `allows at most ${max} characters; "${text}" has ${text.length}`);
      if (!field.isMultiline()) want(!text.includes("\n"), "is one line; remove the line breaks");
      field.setText(text || undefined);
    } else if (type === "checkbox") {
      want(typeof value === "boolean", "takes true or false");
      value ? field.check() : field.uncheck();
    } else if (type === "radio") {
      if (value === null || value === false) field.clear();
      else {
        const opts = field.getOptions();
        want(opts.includes(String(value)), `has no option "${value}"; options: ${opts.join(", ")}`);
        field.select(String(value));
      }
    } else if (type === "dropdown" || type === "list") {
      const opts = field.getOptions();
      const picks = value === null ? [] : Array.isArray(value) ? value.map(String) : [String(value)];
      want(picks.length <= 1 || field.isMultiselect(), "takes one value");
      const editable = type === "dropdown" && field.isEditable();
      for (const pick of picks) want(editable || opts.includes(pick), `has no option "${pick}"; options: ${opts.join(", ")}`);
      if (!picks.length) field.clear();
      else field.select(picks.length === 1 ? picks[0] : picks);
    } else {
      fail(`field "${name}" is a ${type} field; this skill fills text, checkbox, radio, dropdown and list fields`);
    }
    filled.push(name);
  }
  return filled;
}

function copyAcroForm(target, source, copiedPages) {
  const sourceAcro = source.catalog.lookupMaybe(PDFName.of("AcroForm"), PDFDict);
  if (!sourceAcro) return [];
  const targetForm = target.getForm();
  const targetAcro = targetForm.acroForm.dict;
  const taken = new Set();
  for (const field of targetForm.getFields()) {
    const parts = field.getName().split(".");
    for (let k = 1; k <= parts.length; k++) taken.add(parts.slice(0, k).join("."));
  }
  const fieldsArray = targetForm.acroForm.Fields() ?? (() => {
    const array = target.context.obj([]);
    targetAcro.set(PDFName.of("Fields"), array);
    return array;
  })();
  const present = new Set();
  for (let i = 0; i < fieldsArray.size(); i++) present.add(fieldsArray.get(i).toString());
  const renamed = [];
  for (const page of copiedPages) {
    const annots = page.node.Annots();
    if (!annots) continue;
    for (let i = 0; i < annots.size(); i++) {
      let ref = annots.get(i);
      let dict = target.context.lookup(ref);
      if (!(dict instanceof PDFDict) || dict.get(PDFName.of("Subtype")) !== PDFName.of("Widget")) continue;
      dict.set(PDFName.of("P"), page.ref);
      for (let depth = 0; depth < 30; depth++) {
        const parent = dict.get(PDFName.of("Parent"));
        if (!parent) break;
        ref = parent;
        dict = target.context.lookup(parent);
      }
      if (present.has(ref.toString())) continue;
      present.add(ref.toString());
      const title = dict.lookupMaybe(PDFName.of("T"), PDFString, PDFHexString);
      if (title) {
        const name = title.decodeText();
        if (taken.has(name)) {
          let n = 2;
          while (taken.has(`${name}_${n}`)) n++;
          dict.set(PDFName.of("T"), PDFHexString.fromText(`${name}_${n}`));
          renamed.push({ from: name, to: `${name}_${n}` });
          taken.add(`${name}_${n}`);
        } else taken.add(name);
      }
      fieldsArray.push(ref);
    }
  }
  for (const key of ["DA", "Q"]) {
    if (!targetAcro.get(PDFName.of(key)) && sourceAcro.get(PDFName.of(key))) targetAcro.set(PDFName.of(key), sourceAcro.get(PDFName.of(key)));
  }
  const sourceDr = sourceAcro.lookupMaybe(PDFName.of("DR"), PDFDict);
  if (sourceDr) {
    const fonts = sourceDr.lookupMaybe(PDFName.of("Font"), PDFDict);
    if (fonts) {
      let targetDr = targetAcro.lookupMaybe(PDFName.of("DR"), PDFDict);
      if (!targetDr) {
        targetDr = target.context.obj({});
        targetAcro.set(PDFName.of("DR"), targetDr);
      }
      let targetFonts = targetDr.lookupMaybe(PDFName.of("Font"), PDFDict);
      if (!targetFonts) {
        targetFonts = target.context.obj({});
        targetDr.set(PDFName.of("Font"), targetFonts);
      }
      const copier = PDFObjectCopier.for(source.context, target.context);
      for (const [key, value] of fonts.entries()) {
        if (!targetFonts.get(key)) targetFonts.set(key, copier.copy(value));
      }
    }
  }
  if (sourceAcro.get(PDFName.of("NeedAppearances"))) targetAcro.set(PDFName.of("NeedAppearances"), sourceAcro.get(PDFName.of("NeedAppearances")));
  return renamed;
}

function pruneFields(doc) {
  const acro = doc.catalog.lookupMaybe(PDFName.of("AcroForm"), PDFDict);
  if (!acro) return;
  const fields = acro.lookupMaybe(PDFName.of("Fields"), PDFArray);
  if (!fields) return;
  const live = new Set();
  for (const page of doc.getPages()) {
    const annots = page.node.Annots();
    if (!annots) continue;
    for (let i = 0; i < annots.size(); i++) {
      let dict = doc.context.lookup(annots.get(i));
      let ref = annots.get(i);
      for (let depth = 0; dict instanceof PDFDict && depth < 30; depth++) {
        live.add(ref.toString());
        const parent = dict.get(PDFName.of("Parent"));
        if (!parent) break;
        ref = parent;
        dict = doc.context.lookup(parent);
      }
    }
  }
  for (let i = fields.size() - 1; i >= 0; i--) {
    if (!live.has(fields.get(i).toString())) fields.remove(i);
  }
}

module.exports = { copyAcroForm, fieldType, fill, listFields, pruneFields };
