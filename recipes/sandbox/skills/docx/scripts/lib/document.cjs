"use strict";

const { strToU8 } = require("fflate");
const x = require("./ooxml.cjs");
const T = require("./template.cjs");

const BLOCK_CONTAINERS = new Set(["w:body", "w:tbl", "w:tr", "w:tc", "w:sdtContent", "w:customXml", "w:hdr", "w:ftr", "w:footnote", "w:endnote", "w:comment", "w:footnotes", "w:endnotes", "w:comments"]);
const BLOCK_WRAPPERS = new Set(["w:sdt"]);

function eachBlockNode(node, visit) {
  for (const entry of x.kids(node)) {
    const tag = x.tagOf(entry);
    if (tag === "w:p" || tag === "w:tbl") visit(entry, x.kids(node));
    if (BLOCK_CONTAINERS.has(tag) || BLOCK_WRAPPERS.has(tag)) eachBlockNode(entry, visit);
  }
}

function paragraphsIn(node) {
  const out = [];
  eachBlockNode(node, (entry, list) => {
    if (x.tagOf(entry) === "w:p") out.push({ p: entry, list });
  });
  return out;
}

function tablesIn(node) {
  const out = [];
  eachBlockNode(node, (entry, list) => {
    if (x.tagOf(entry) === "w:tbl") out.push({ tbl: entry, list });
  });
  return out;
}

function walk(node, visit) {
  if (!node || typeof node !== "object" || "#text" in node) return;
  const tag = x.tagOf(node);
  if (!tag) return;
  visit(node, tag);
  const list = node[tag];
  if (Array.isArray(list)) for (const entry of list) walk(entry, visit);
}

function clone(node) {
  return structuredClone(node);
}

function partXml(text) {
  return x.parseXml(text);
}

class Document {
  constructor(pkg) {
    this.pkg = pkg;
    const main = pkg.rels("").find((rel) => rel.type === T.REL_TYPES.document);
    this.mainPath = main?.part ?? "word/document.xml";
    if (!pkg.files.has(this.mainPath)) throw new Error("not a Word document: word/document.xml is missing");
    this.tree = pkg.xml(this.mainPath);
    this.root = x.rootOf(this.tree);
    if (x.tagOf(this.root) !== "w:document") throw new Error(`not a Word document: the main part is <${x.tagOf(this.root)}>`);
    this.body = x.child(this.root, "w:body");
    if (!this.body) throw new Error("the document has no body");
    this.counter = undefined;
    this.docPrCounter = undefined;
  }

  static open(bytes) {
    const pkg = x.Package.open(bytes);
    const types = x.rootOf(pkg.xml("[Content_Types].xml"));
    const kinds = x.children(types, "Override").map((node) => x.attrs(node).ContentType);
    if (!kinds.includes(T.CONTENT_TYPES.document) && !kinds.includes(T.CONTENT_TYPES.macroDocument)) {
      if (kinds.some((kind) => /spreadsheetml|presentationml/.test(kind))) throw new Error("this is an Excel or PowerPoint file, not a Word document");
      if (kinds.some((kind) => /template\.main/.test(kind))) throw new Error("Word templates (.dotx, .dotm) are not supported; save the template as a .docx first");
    }
    return new Document(pkg);
  }

  static blank(options = {}) {
    const files = new Map(Object.entries(T.blankPackage(options)).map(([name, text]) => [name, strToU8(text)]));
    return new Document(new x.Package(files));
  }

  rels() {
    return this.pkg.rels(this.mainPath);
  }

  relTarget(id) {
    return this.rels().find((rel) => rel.id === id);
  }

  partOf(type) {
    return this.rels().find((rel) => rel.type === type)?.part;
  }

  partsOf(type) {
    return this.rels().filter((rel) => rel.type === type).map((rel) => rel.part);
  }

  treeOf(path) {
    return path ? this.pkg.xml(path) : undefined;
  }

  storyRoots(scope) {
    const roots = [{ part: this.mainPath, node: this.body }];
    if (scope !== "all") return roots;
    for (const type of [T.REL_TYPES.header, T.REL_TYPES.footer]) {
      for (const part of this.partsOf(type)) {
        const tree = this.treeOf(part);
        if (tree) roots.push({ part, node: x.rootOf(tree) });
      }
    }
    for (const type of ["footnotes", "endnotes"]) {
      const part = this.partOf(`http://schemas.openxmlformats.org/officeDocument/2006/relationships/${type}`);
      const tree = this.treeOf(part);
      if (tree) roots.push({ part, node: x.rootOf(tree) });
    }
    return roots;
  }

  allTrees() {
    const out = [this.tree];
    for (const rel of this.rels()) {
      if (rel.mode === "External" || !rel.part?.endsWith(".xml")) continue;
      const tree = this.treeOf(rel.part);
      if (tree) out.push(tree);
    }
    return out;
  }

  paragraphs() {
    return paragraphsIn(this.body);
  }

  tables() {
    return tablesIn(this.body);
  }

  ensurePart(type, path, contentType, text) {
    const existing = this.partOf(type);
    if (existing) return existing;
    this.pkg.setXml(path, partXml(text));
    this.pkg.setOverride(path, contentType);
    const target = path.startsWith("word/") ? path.slice(5) : `/${path}`;
    this.pkg.addRel(this.mainPath, type, target);
    return path;
  }

  stylesRoot() {
    const path = this.ensurePart(T.REL_TYPES.styles, "word/styles.xml", T.CONTENT_TYPES.styles, T.HEAD + `<w:styles ${T.ROOT_NS}></w:styles>`);
    return x.rootOf(this.pkg.xml(path));
  }

  styles() {
    const path = this.partOf(T.REL_TYPES.styles);
    const tree = this.treeOf(path);
    const map = new Map();
    if (!tree) return map;
    for (const node of x.children(x.rootOf(tree), "w:style")) {
      const a = x.attrs(node);
      const nameNode = x.child(node, "w:name");
      const basedOn = x.child(node, "w:basedOn");
      map.set(a["w:styleId"], {
        id: a["w:styleId"],
        type: a["w:type"],
        name: nameNode ? x.attrs(nameNode)["w:val"] : a["w:styleId"],
        basedOn: basedOn ? x.attrs(basedOn)["w:val"] : undefined,
        isDefault: a["w:default"] === "1" || a["w:default"] === "true",
        node,
      });
    }
    return map;
  }

  findStyle(name, type) {
    const wanted = String(name).toLowerCase();
    for (const style of this.styles().values()) {
      if (type && style.type !== type) continue;
      if (style.id.toLowerCase() === wanted || style.name.toLowerCase() === wanted) return style;
    }
    return undefined;
  }

  ensureStyle(key) {
    const found = this.findStyle(key);
    if (found) return found.id;
    const def = T.STYLE_DEFS[key];
    if (!def) throw new Error(`no style named "${key}"`);
    const root = this.stylesRoot();
    const node = x.rootOf(partXml(`<w:styles xmlns:w="${T.NS.w}">${def}</w:styles>`));
    const style = x.children(node, "w:style")[0];
    delete x.attrs(style)["w:default"];
    x.kids(root).push(style);
    const basedOn = x.child(style, "w:basedOn");
    if (basedOn) {
      const parent = x.attrs(basedOn)["w:val"];
      if (!this.styles().has(parent)) x.removeChildren(style, "w:basedOn");
    }
    return x.attrs(style)["w:styleId"];
  }

  paragraphStyle(name) {
    const style = this.findStyle(name, "paragraph");
    if (style) return style.id;
    const key = String(name).toLowerCase();
    if (T.STYLE_DEFS[key]) return this.ensureStyle(key);
    const names = [...this.styles().values()].filter((entry) => entry.type === "paragraph").map((entry) => entry.name);
    throw new Error(`no paragraph style named "${name}"; the document has: ${names.join(", ")}`);
  }

  headingStyle(level) {
    if (!(level >= 1 && level <= 6)) throw new Error("heading level must be 1 to 6");
    return this.ensureStyle(`heading ${level}`);
  }

  styleChain(id) {
    const styles = this.styles();
    const chain = [];
    const seen = new Set();
    let current = id ? styles.get(id) : [...styles.values()].find((entry) => entry.type === "paragraph" && entry.isDefault);
    while (current && !seen.has(current.id)) {
      seen.add(current.id);
      chain.push(current);
      current = current.basedOn ? styles.get(current.basedOn) : undefined;
    }
    return chain;
  }

  headingLevel(p) {
    const pPr = x.child(p, "w:pPr");
    const fromOutline = (node) => {
      const outline = node && x.child(node, "w:outlineLvl");
      if (!outline) return undefined;
      const level = Number(x.attrs(outline)["w:val"]);
      return level >= 0 && level < 9 ? level + 1 : 0;
    };
    const own = fromOutline(pPr);
    if (own !== undefined) return own;
    const styleNode = pPr && x.child(pPr, "w:pStyle");
    for (const style of this.styleChain(styleNode ? x.attrs(styleNode)["w:val"] : undefined)) {
      const name = style.name.toLowerCase();
      if (name === "title") return 1;
      const match = /^heading (\d)$/.exec(name);
      if (match) return Number(match[1]);
      const level = fromOutline(x.child(style.node, "w:pPr"));
      if (level !== undefined) return level;
    }
    return 0;
  }

  styleName(p) {
    const pPr = x.child(p, "w:pPr");
    const styleNode = pPr && x.child(pPr, "w:pStyle");
    if (!styleNode) return undefined;
    const id = x.attrs(styleNode)["w:val"];
    return this.styles().get(id)?.name ?? id;
  }

  numberingRoot(create) {
    let path = this.partOf(T.REL_TYPES.numbering);
    if (!path) {
      if (!create) return undefined;
      path = this.ensurePart(T.REL_TYPES.numbering, "word/numbering.xml", T.CONTENT_TYPES.numbering, T.HEAD + `<w:numbering ${T.ROOT_NS}></w:numbering>`);
    }
    return x.rootOf(this.pkg.xml(path));
  }

  listInfo(p) {
    const pPr = x.child(p, "w:pPr");
    let numPr = pPr && x.child(pPr, "w:numPr");
    if (!numPr) {
      const styleNode = pPr && x.child(pPr, "w:pStyle");
      for (const style of this.styleChain(styleNode ? x.attrs(styleNode)["w:val"] : undefined)) {
        const stylePPr = x.child(style.node, "w:pPr");
        numPr = stylePPr && x.child(stylePPr, "w:numPr");
        if (numPr) break;
      }
    }
    if (!numPr) return undefined;
    const numIdNode = x.child(numPr, "w:numId");
    const ilvlNode = x.child(numPr, "w:ilvl");
    const numId = numIdNode ? x.attrs(numIdNode)["w:val"] : undefined;
    if (!numId || numId === "0") return undefined;
    const level = ilvlNode ? Number(x.attrs(ilvlNode)["w:val"]) : 0;
    const root = this.numberingRoot(false);
    let format = "decimal";
    if (root) {
      const num = x.children(root, "w:num").find((node) => x.attrs(node)["w:numId"] === numId);
      const abstractRef = num && x.child(num, "w:abstractNumId");
      const abstractId = abstractRef && x.attrs(abstractRef)["w:val"];
      const abstract = x.children(root, "w:abstractNum").find((node) => x.attrs(node)["w:abstractNumId"] === abstractId);
      const lvl = abstract && x.children(abstract, "w:lvl").find((node) => Number(x.attrs(node)["w:ilvl"]) === level);
      const fmt = lvl && x.child(lvl, "w:numFmt");
      if (fmt) format = x.attrs(fmt)["w:val"];
    }
    return { numId, level, ordered: format !== "bullet" && format !== "none" };
  }

  newList(kind) {
    const root = this.numberingRoot(true);
    const name = kind === "bullet" ? "OpenNeko bullets" : "OpenNeko numbers";
    let abstract = x.children(root, "w:abstractNum").find((node) => {
      const nameNode = x.child(node, "w:name");
      return nameNode && x.attrs(nameNode)["w:val"] === name;
    });
    if (!abstract) {
      const ids = x.children(root, "w:abstractNum").map((node) => Number(x.attrs(node)["w:abstractNumId"]) || 0);
      const id = ids.length ? Math.max(...ids) + 1 : 0;
      const wrapper = x.rootOf(partXml(`<w:numbering xmlns:w="${T.NS.w}">${T.abstractNumXml(id, kind)}</w:numbering>`));
      abstract = x.children(wrapper, "w:abstractNum")[0];
      const list = x.kids(root);
      const firstNum = list.findIndex((node) => x.tagOf(node) === "w:num" || x.tagOf(node) === "w:numIdMacAtCleanup");
      list.splice(firstNum < 0 ? list.length : firstNum, 0, abstract);
    }
    const abstractId = x.attrs(abstract)["w:abstractNumId"];
    const numIds = x.children(root, "w:num").map((node) => Number(x.attrs(node)["w:numId"]) || 0);
    const numId = String(numIds.length ? Math.max(...numIds) + 1 : 1);
    const num = x.el("w:num", { "w:numId": numId }, [x.el("w:abstractNumId", { "w:val": abstractId })]);
    if (kind !== "bullet") num["w:num"].push(x.el("w:lvlOverride", { "w:ilvl": "0" }, [x.el("w:startOverride", { "w:val": "1" })]));
    const list = x.kids(root);
    const cleanup = list.findIndex((node) => x.tagOf(node) === "w:numIdMacAtCleanup");
    list.splice(cleanup < 0 ? list.length : cleanup, 0, num);
    return numId;
  }

  nextId() {
    if (this.counter === undefined) {
      let max = 0;
      for (const tree of this.allTrees()) {
        for (const node of tree) {
          walk(node, (entry) => {
            const id = Number(entry[":@"]?.["w:id"]);
            if (Number.isFinite(id) && id > max) max = id;
          });
        }
      }
      this.counter = max;
    }
    this.counter += 1;
    return String(this.counter);
  }

  nextDocPrId() {
    if (this.docPrCounter === undefined) {
      let max = 0;
      for (const tree of this.allTrees()) {
        for (const node of tree) {
          walk(node, (entry, tag) => {
            if (tag !== "wp:docPr") return;
            const id = Number(entry[":@"]?.id);
            if (Number.isFinite(id) && id > max) max = id;
          });
        }
      }
      this.docPrCounter = max;
    }
    this.docPrCounter += 1;
    return this.docPrCounter;
  }

  ensureRootNamespaces() {
    const a = x.attrs(this.root);
    for (const prefix of ["w", "r", "wp", "a", "pic"]) {
      if (!a[`xmlns:${prefix}`]) a[`xmlns:${prefix}`] = T.NS[prefix];
    }
  }

  addImage(bytes, kind) {
    const types = x.rootOf(this.pkg.xml("[Content_Types].xml"));
    const ext = kind === "jpeg" ? "jpeg" : kind;
    if (!x.children(types, "Default").some((node) => x.attrs(node).Extension?.toLowerCase() === ext)) {
      x.kids(types).unshift(x.el("Default", { Extension: ext, ContentType: T.IMAGE_TYPES[kind] }));
    }
    let n = 1;
    while (this.pkg.files.has(`word/media/openneko-image${n}.${ext}`)) n++;
    const path = `word/media/openneko-image${n}.${ext}`;
    this.pkg.files.set(path, bytes);
    return this.pkg.addRel(this.mainPath, T.REL_TYPES.image, `media/openneko-image${n}.${ext}`);
  }

  addHyperlink(url, part) {
    const owner = part ?? this.mainPath;
    const existing = this.pkg.rels(owner).find((rel) => rel.type === T.REL_TYPES.hyperlink && rel.target === url);
    if (existing) return existing.id;
    const id = this.pkg.addRel(owner, T.REL_TYPES.hyperlink, url);
    const rels = this.pkg.rels(owner).find((rel) => rel.id === id);
    x.attrs(rels.node).TargetMode = "External";
    return id;
  }

  commentsRoot(create) {
    let path = this.partOf(T.REL_TYPES.comments);
    if (!path) {
      if (!create) return undefined;
      path = this.ensurePart(T.REL_TYPES.comments, "word/comments.xml", T.CONTENT_TYPES.comments, T.HEAD + `<w:comments ${T.ROOT_NS}></w:comments>`);
    }
    return x.rootOf(this.pkg.xml(path));
  }

  settingsRoot() {
    const path = this.ensurePart(T.REL_TYPES.settings, "word/settings.xml", T.CONTENT_TYPES.settings, T.HEAD + `<w:settings ${T.ROOT_NS}></w:settings>`);
    return x.rootOf(this.pkg.xml(path));
  }

  setSetting(name, on) {
    const root = this.settingsRoot();
    x.removeChildren(root, name);
    if (on) {
      const order = ["w:writeProtection", "w:view", "w:zoom", "w:removePersonalInformation", "w:removeDateAndTime", "w:doNotDisplayPageBoundaries", "w:displayBackgroundShape", "w:printPostScriptOverText", "w:printFractionalCharacterWidth", "w:printFormsData", "w:embedTrueTypeFonts", "w:embedSystemFonts", "w:saveSubsetFonts", "w:saveFormsData", "w:mirrorMargins", "w:alignBordersAndEdges", "w:bordersDoNotSurroundHeader", "w:bordersDoNotSurroundFooter", "w:gutterAtTop", "w:hideSpellingErrors", "w:hideGrammaticalErrors", "w:activeWritingStyle", "w:proofState", "w:formsDesign", "w:attachedTemplate", "w:linkStyles", "w:stylePaneFormatFilter", "w:stylePaneSortMethod", "w:documentType", "w:mailMerge", "w:revisionView", "w:trackRevisions", "w:doNotTrackMoves", "w:doNotTrackFormatting", "w:documentProtection", "w:autoFormatOverride", "w:styleLockTheme", "w:styleLockQFSet", "w:defaultTabStop", "w:updateFields", "w:hdrShapeDefaults", "w:footnotePr", "w:endnotePr", "w:compat", "w:docVars", "w:rsids", "m:mathPr", "w:attachedSchema", "w:themeFontLang", "w:clrSchemeMapping", "w:doNotIncludeSubdocsInStats", "w:doNotAutoCompressPictures", "w:forceUpgrade", "w:captions", "w:readModeInkLockDown", "w:smartTagType", "sl:schemaLibrary", "w:shapeDefaults", "w:doNotEmbedSmartTags", "w:decimalSymbol", "w:listSeparator"];
      x.insertOrdered(root, x.el(name), order);
    }
  }

  setting(name) {
    const path = this.partOf(T.REL_TYPES.settings);
    const tree = this.treeOf(path);
    if (!tree) return false;
    const node = x.child(x.rootOf(tree), name);
    if (!node) return false;
    const value = x.attrs(node)["w:val"];
    return value === undefined || !["0", "false", "off"].includes(value);
  }

  coreRoot() {
    const rel = this.pkg.rels("").find((entry) => entry.type === T.REL_TYPES.core);
    const tree = rel && this.pkg.xml(rel.part);
    return tree ? x.rootOf(tree) : undefined;
  }

  properties() {
    const root = this.coreRoot();
    if (!root) return {};
    const read = (name) => {
      const node = x.child(root, name);
      return node ? x.textOf(node) : undefined;
    };
    return { title: read("dc:title"), subject: read("dc:subject"), author: read("dc:creator"), keywords: read("cp:keywords"), modified: read("dcterms:modified") };
  }

  ensureCore() {
    const existing = this.coreRoot();
    if (existing) return existing;
    const path = "docProps/core.xml";
    this.pkg.setXml(path, partXml(`${T.HEAD}<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"></cp:coreProperties>`));
    this.pkg.setOverride(path, T.CONTENT_TYPES.core);
    this.pkg.addRel("", T.REL_TYPES.core, path);
    return this.coreRoot();
  }

  setProperties(values) {
    const root = this.ensureCore();
    const map = { title: "dc:title", subject: "dc:subject", author: "dc:creator", keywords: "cp:keywords", description: "dc:description" };
    for (const [key, value] of Object.entries(values)) {
      const name = map[key];
      if (!name) throw new Error(`unknown property "${key}"; use title, subject, author, keywords or description`);
      x.removeChildren(root, name);
      if (value !== null && value !== undefined && value !== "") x.kids(root).push(x.el(name, null, [x.textNode(value)]));
    }
  }

  touchModified(author, date) {
    const root = this.coreRoot();
    if (!root) return;
    x.removeChildren(root, "dcterms:modified");
    x.kids(root).push(x.el("dcterms:modified", { "xsi:type": "dcterms:W3CDTF" }, [x.textNode(date)]));
    if (!x.attrs(root)["xmlns:dcterms"]) x.attrs(root)["xmlns:dcterms"] = "http://purl.org/dc/terms/";
    if (!x.attrs(root)["xmlns:xsi"]) x.attrs(root)["xmlns:xsi"] = "http://www.w3.org/2001/XMLSchema-instance";
    if (author) {
      x.removeChildren(root, "cp:lastModifiedBy");
      x.kids(root).push(x.el("cp:lastModifiedBy", null, [x.textNode(author)]));
    }
  }

  sectPr() {
    return x.child(this.body, "w:sectPr");
  }

  textWidthTwips() {
    const sect = this.sectPr();
    const size = sect && x.child(sect, "w:pgSz");
    const margin = sect && x.child(sect, "w:pgMar");
    const width = size ? Number(x.attrs(size)["w:w"]) : 11906;
    const left = margin ? Number(x.attrs(margin)["w:left"]) : 1440;
    const right = margin ? Number(x.attrs(margin)["w:right"]) : 1440;
    const text = width - left - right;
    return Number.isFinite(text) && text > 0 ? text : 9026;
  }

  bytes() {
    return this.pkg.bytes();
  }
}

module.exports = { Document, clone, eachBlockNode, paragraphsIn, tablesIn, walk };
