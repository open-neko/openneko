"use strict";

const crypto = require("node:crypto");
const { strToU8 } = require("fflate");
const x = require("./ooxml.cjs");
const T = require("./template.cjs");

const SHARED_ON_COPY = new Set([T.REL_TYPES.slideLayout, T.REL_TYPES.image, `${T.REL}/audio`, `${T.REL}/video`, "http://schemas.microsoft.com/office/2007/relationships/media", T.REL_TYPES.hyperlink, T.REL_TYPES.slide]);
const DROPPED_ON_COPY = new Set([T.REL_TYPES.notesSlide, "http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments", "http://schemas.microsoft.com/office/2018/10/relationships/comments"]);

function partXml(text) {
  return x.parseXml(text);
}

function clone(node) {
  return JSON.parse(JSON.stringify(node));
}

function walk(node, visit) {
  if (!node || typeof node !== "object") return;
  if (visit(node) === false) return;
  const name = x.tagOf(node);
  if (!name || name === "#text") return;
  const list = node[name];
  if (Array.isArray(list)) for (const entry of list) walk(entry, visit);
}

function decodeTarget(pkg, path) {
  if (pkg.files.has(path)) return path;
  try {
    const decoded = decodeURIComponent(path);
    if (pkg.files.has(decoded)) return decoded;
  } catch {}
  return path;
}

function relsOf(pkg, part) {
  return pkg.rels(part).map((rel) => ({ ...rel, part: rel.part && decodeTarget(pkg, rel.part) }));
}

function relativeTarget(from, to) {
  const a = from.split("/").slice(0, -1);
  const b = to.split("/");
  let i = 0;
  while (i < a.length && i < b.length - 1 && a[i] === b[i]) i++;
  return [...Array(a.length - i).fill(".."), ...b.slice(i)].join("/");
}

function numberOf(path) {
  const match = /(\d+)\.[a-z]+$/i.exec(path);
  return match ? Number(match[1]) : 0;
}

class Deck {
  constructor(pkg) {
    this.pkg = pkg;
    const rel = pkg.rels("").find((entry) => entry.type === T.REL_TYPES.document);
    if (!rel) throw new Error("not a presentation: the package has no main part");
    this.presPath = decodeTarget(pkg, rel.part);
    const root = this.presRoot();
    if (!root || x.tagOf(root) !== "p:presentation") throw new Error("not a presentation: the main part is not p:presentation");
  }

  static open(bytes) {
    return new Deck(x.Package.open(bytes));
  }

  static blank(size) {
    const [cx, cy] = size;
    const files = new Map(Object.entries(T.blankParts(cx, cy)).map(([path, text]) => [path, strToU8(text)]));
    return new Deck(new x.Package(files));
  }

  presRoot() {
    return x.rootOf(this.pkg.xml(this.presPath));
  }

  mainContentType() {
    const root = x.rootOf(this.pkg.xml("[Content_Types].xml"));
    const node = x.children(root, "Override").find((entry) => x.attrs(entry).PartName === `/${this.presPath}`);
    return node ? x.attrs(node).ContentType : undefined;
  }

  isMacroEnabled() {
    return relsOf(this.pkg, this.presPath).some((rel) => rel.type === T.REL_TYPES.vbaProject);
  }

  prepareFor(extension) {
    const notes = [];
    const ext = extension.toLowerCase();
    if (ext === ".pptm") {
      this.pkg.setOverride(this.presPath, T.CONTENT_TYPES.macro);
      return notes;
    }
    if (ext === ".potm") {
      this.pkg.setOverride(this.presPath, T.CONTENT_TYPES.macroTemplate);
      return notes;
    }
    if (this.isMacroEnabled()) {
      for (const rel of relsOf(this.pkg, this.presPath).filter((entry) => entry.type === T.REL_TYPES.vbaProject)) this.pkg.removeRel(this.presPath, rel.id);
      this.collectGarbage();
      notes.push(`the macros were removed, because a ${ext} file cannot hold them; save as .pptm to keep them`);
    }
    this.pkg.setOverride(this.presPath, ext === ".potx" ? T.CONTENT_TYPES.template : T.CONTENT_TYPES.presentation);
    return notes;
  }

  size() {
    const node = x.child(this.presRoot(), "p:sldSz");
    const a = node ? x.attrs(node) : {};
    return { cx: Number(a.cx) || 12192000, cy: Number(a.cy) || 6858000 };
  }

  slides() {
    const list = x.child(this.presRoot(), "p:sldIdLst");
    if (!list) return [];
    const rels = new Map(relsOf(this.pkg, this.presPath).map((rel) => [rel.id, rel]));
    return x.children(list, "p:sldId").map((node, i) => {
      const a = x.attrs(node);
      const rel = rels.get(a["r:id"]);
      return { number: i + 1, id: Number(a.id), rid: a["r:id"], path: rel?.part, node };
    }).filter((slide) => slide.path && this.pkg.files.has(slide.path));
  }

  slide(number) {
    const slides = this.slides();
    const n = Number(number);
    if (!Number.isInteger(n) || n < 1 || n > slides.length) throw new Error(`slide must be 1 to ${slides.length}, got ${JSON.stringify(number)}`);
    return slides[n - 1];
  }

  slideRoot(path) {
    return x.rootOf(this.pkg.xml(path));
  }

  spTree(path) {
    return x.child(x.child(this.slideRoot(path), "p:cSld"), "p:spTree");
  }

  relTarget(part, type) {
    return relsOf(this.pkg, part).find((rel) => rel.type === type)?.part;
  }

  layoutOf(slidePath) {
    return this.relTarget(slidePath, T.REL_TYPES.slideLayout);
  }

  masterOf(layoutPath) {
    return this.relTarget(layoutPath, T.REL_TYPES.slideMaster);
  }

  masters() {
    const list = x.child(this.presRoot(), "p:sldMasterIdLst");
    if (!list) return [];
    const rels = new Map(relsOf(this.pkg, this.presPath).map((rel) => [rel.id, rel]));
    return x.children(list, "p:sldMasterId").map((node) => rels.get(x.attrs(node)["r:id"])?.part).filter(Boolean);
  }

  layouts() {
    const out = [];
    this.masters().forEach((master, m) => {
      const rels = new Map(relsOf(this.pkg, master).map((rel) => [rel.id, rel]));
      const list = x.child(x.rootOf(this.pkg.xml(master)), "p:sldLayoutIdLst");
      if (!list) return;
      for (const node of x.children(list, "p:sldLayoutId")) {
        const path = rels.get(x.attrs(node)["r:id"])?.part;
        if (!path || !this.pkg.files.has(path)) continue;
        const root = x.rootOf(this.pkg.xml(path));
        const cSld = x.child(root, "p:cSld");
        out.push({ number: out.length + 1, name: x.attrs(cSld).name ?? "", type: x.attrs(root).type ?? "cust", master: m + 1, masterPath: master, path });
      }
    });
    return out;
  }

  findLayout(wanted) {
    const layouts = this.layouts();
    if (!layouts.length) throw new Error("the presentation has no slide layouts");
    if (wanted === undefined || wanted === null) {
      return layouts.find((l) => l.type === "obj") ?? layouts.find((l) => /title and content/i.test(l.name)) ?? layouts[Math.min(1, layouts.length - 1)];
    }
    if (typeof wanted === "number") {
      if (!Number.isInteger(wanted) || wanted < 1 || wanted > layouts.length) throw new Error(`layout must be 1 to ${layouts.length}, got ${wanted}`);
      return layouts[wanted - 1];
    }
    const key = String(wanted).trim().toLowerCase();
    const found = layouts.find((l) => l.name.toLowerCase() === key) ?? layouts.find((l) => l.type.toLowerCase() === key);
    if (found) return found;
    const partial = layouts.filter((l) => l.name.toLowerCase().includes(key));
    if (partial.length === 1) return partial[0];
    throw new Error(`no layout named "${wanted}"; the layouts are: ${layouts.map((l) => `${l.number} "${l.name}"`).join(", ")}`);
  }

  freePart(folder, stem, ext) {
    let n = 1;
    while (this.pkg.files.has(`${folder}/${stem}${n}.${ext}`)) n++;
    return `${folder}/${stem}${n}.${ext}`;
  }

  ensureDefault(ext, type) {
    const root = x.rootOf(this.pkg.xml("[Content_Types].xml"));
    const found = x.children(root, "Default").find((node) => (x.attrs(node).Extension ?? "").toLowerCase() === ext);
    if (!found) x.kids(root).unshift(x.el("Default", { Extension: ext, ContentType: type }));
  }

  sldIdList() {
    const root = this.presRoot();
    let list = x.child(root, "p:sldIdLst");
    if (!list) list = x.insertOrdered(root, x.el("p:sldIdLst"), ["p:sldMasterIdLst", "p:notesMasterIdLst", "p:handoutMasterIdLst", "p:sldIdLst", "p:sldSz", "p:notesSz"]);
    return list;
  }

  registerSlide(path, position) {
    const rid = this.pkg.addRel(this.presPath, T.REL_TYPES.slide, relativeTarget(this.presPath, path));
    const list = this.sldIdList();
    const used = x.children(list, "p:sldId").map((node) => Number(x.attrs(node).id));
    const id = Math.max(255, ...used) + 1;
    const node = x.el("p:sldId", { id, "r:id": rid });
    const ids = x.children(list, "p:sldId");
    const at = position === undefined || position > ids.length ? ids.length : Math.max(0, position - 1);
    const anchor = ids[at];
    const kids = x.kids(list);
    kids.splice(anchor ? kids.indexOf(anchor) : kids.length, 0, node);
    this.syncSections();
    return { id, rid, path };
  }

  addSlide(layout, position) {
    const path = this.freePart("ppt/slides", "slide", "xml");
    this.pkg.setXml(path, partXml(T.slideXml()));
    this.pkg.setOverride(path, T.CONTENT_TYPES.slide);
    this.pkg.addRel(path, T.REL_TYPES.slideLayout, relativeTarget(path, layout.path));
    this.registerSlide(path, position);
    return path;
  }

  copyPart(source, map) {
    if (map.has(source)) return map.get(source);
    const ext = source.split(".").pop();
    const folder = source.split("/").slice(0, -1).join("/");
    const stem = source.split("/").pop().replace(/\d*\.[^.]+$/, "");
    const target = this.freePart(folder, stem, ext);
    map.set(source, target);
    const tree = this.pkg.trees.get(source);
    if (tree) this.pkg.setXml(target, clone(tree));
    else this.pkg.files.set(target, this.pkg.files.get(source));
    const type = this.overrideOf(source);
    if (type) this.pkg.setOverride(target, type);
    this.copyRels(source, target, map);
    return target;
  }

  copyRels(source, target, map) {
    const rels = relsOf(this.pkg, source);
    if (!rels.length) return;
    const tree = clone(this.pkg.xml(`${source.slice(0, source.lastIndexOf("/") + 1)}_rels/${source.split("/").pop()}.rels`));
    const root = x.rootOf(tree);
    const dropped = [];
    for (const node of x.children(root, "Relationship")) {
      const a = x.attrs(node);
      const rel = rels.find((entry) => entry.id === a.Id);
      if (!rel || rel.mode === "External" || !rel.part) continue;
      if (DROPPED_ON_COPY.has(rel.type)) {
        dropped.push(node);
        continue;
      }
      if (SHARED_ON_COPY.has(rel.type) || !this.pkg.files.has(rel.part)) {
        a.Target = relativeTarget(target, rel.part);
        continue;
      }
      a.Target = relativeTarget(target, this.copyPart(rel.part, map));
    }
    for (const node of dropped) x.kids(root).splice(x.kids(root).indexOf(node), 1);
    this.pkg.setXml(`${target.slice(0, target.lastIndexOf("/") + 1)}_rels/${target.split("/").pop()}.rels`, tree);
  }

  overrideOf(part) {
    const root = x.rootOf(this.pkg.xml("[Content_Types].xml"));
    const node = x.children(root, "Override").find((entry) => x.attrs(entry).PartName === `/${part}`);
    return node ? x.attrs(node).ContentType : undefined;
  }

  duplicateSlide(source, position) {
    const map = new Map();
    const path = this.copyPart(source, map);
    const notes = this.notesText(source);
    this.registerSlide(path, position);
    if (notes) this.setNotes(path, notes.paragraphs);
    return path;
  }

  removeSlide(slide) {
    const list = this.sldIdList();
    x.kids(list).splice(x.kids(list).indexOf(slide.node), 1);
    const custom = x.child(this.presRoot(), "p:custShowLst");
    if (custom) {
      walk(custom, (node) => {
        const name = x.tagOf(node);
        if (name === "p:sldLst") {
          const kids = x.kids(node);
          for (let i = kids.length - 1; i >= 0; i--) if (x.tagOf(kids[i]) === "p:sld" && x.attrs(kids[i])["r:id"] === slide.rid) kids.splice(i, 1);
        }
      });
    }
    this.pkg.removeRel(this.presPath, slide.rid);
    for (const other of this.slides()) {
      for (const rel of relsOf(this.pkg, other.path)) {
        if (rel.part !== slide.path) continue;
        this.pkg.removeRel(other.path, rel.id);
        walk(this.slideRoot(other.path), (node) => {
          const kids = x.kids(node);
          if (!Array.isArray(kids)) return;
          for (let i = kids.length - 1; i >= 0; i--) {
            const name = x.tagOf(kids[i]);
            if ((name === "a:hlinkClick" || name === "a:hlinkMouseOver") && kids[i][":@"]?.["r:id"] === rel.id) kids.splice(i, 1);
          }
        });
      }
    }
    this.syncSections();
    this.collectGarbage();
  }

  moveSlide(slide, position) {
    const list = this.sldIdList();
    const kids = x.kids(list);
    kids.splice(kids.indexOf(slide.node), 1);
    const ids = x.children(list, "p:sldId");
    const anchor = ids[position - 1];
    kids.splice(anchor ? kids.indexOf(anchor) : kids.length, 0, slide.node);
    this.syncSections(slide.id);
  }

  sectionList() {
    let found;
    walk(this.presRoot(), (node) => {
      if (found) return false;
      if ((x.tagOf(node) ?? "").endsWith(":sectionLst")) {
        found = node;
        return false;
      }
    });
    return found;
  }

  dropSections() {
    const root = this.presRoot();
    const extLst = x.child(root, "p:extLst");
    if (!extLst) return;
    const list = x.kids(extLst);
    for (let i = list.length - 1; i >= 0; i--) {
      let has = false;
      walk(list[i], (node) => {
        if ((x.tagOf(node) ?? "").endsWith(":sectionLst")) has = true;
      });
      if (has) list.splice(i, 1);
    }
    if (!list.length) x.removeChildren(root, "p:extLst");
  }

  syncSections(moved) {
    const list = this.sectionList();
    if (!list) return;
    const prefix = x.tagOf(list).split(":")[0];
    const sections = x.kids(list).filter((node) => x.tagOf(node) === `${prefix}:section`);
    if (!sections.length) return;
    const owner = new Map();
    sections.forEach((section, s) => {
      const ids = x.child(section, `${prefix}:sldIdLst`);
      if (ids) for (const node of x.children(ids, `${prefix}:sldId`)) owner.set(Number(x.attrs(node).id), s);
    });
    if (moved !== undefined) owner.delete(moved);
    const order = this.slides().map((slide) => slide.id);
    const assigned = sections.map(() => []);
    let running = 0;
    for (const id of order) {
      let s = owner.has(id) ? owner.get(id) : running;
      if (s < running) s = running;
      running = s;
      assigned[s].push(id);
    }
    sections.forEach((section, s) => {
      let ids = x.child(section, `${prefix}:sldIdLst`);
      if (!ids) {
        ids = x.el(`${prefix}:sldIdLst`);
        x.kids(section).unshift(ids);
      }
      ids[x.tagOf(ids)] = assigned[s].map((id) => x.el(`${prefix}:sldId`, { id }));
    });
  }

  reachable() {
    const seen = new Set();
    const queue = relsOf(this.pkg, "").filter((rel) => rel.part).map((rel) => rel.part);
    while (queue.length) {
      const part = queue.pop();
      if (seen.has(part) || !this.pkg.files.has(part)) continue;
      seen.add(part);
      for (const rel of relsOf(this.pkg, part)) if (rel.mode !== "External" && rel.part) queue.push(rel.part);
    }
    return seen;
  }

  collectGarbage() {
    const keep = this.reachable();
    const relsFor = (part) => `${part.slice(0, part.lastIndexOf("/") + 1)}_rels/${part.split("/").pop()}.rels`;
    const keepRels = new Set(["_rels/.rels", ...[...keep].map(relsFor)]);
    for (const path of [...this.pkg.files.keys()]) {
      if (path === "[Content_Types].xml" || keep.has(path) || keepRels.has(path)) continue;
      if (path.endsWith(".rels") || /^(ppt|docProps)\//.test(path)) {
        this.pkg.remove(path);
        this.pkg.setOverride(path, null);
      }
    }
  }

  notesPath(slidePath) {
    return this.relTarget(slidePath, T.REL_TYPES.notesSlide);
  }

  notesBody(notesPath) {
    const tree = x.child(x.child(x.rootOf(this.pkg.xml(notesPath)), "p:cSld"), "p:spTree");
    for (const sp of x.children(tree, "p:sp")) {
      const ph = x.child(x.child(x.child(sp, "p:nvSpPr"), "p:nvPr"), "p:ph");
      if (ph && x.attrs(ph).type === "body") return sp;
    }
    return undefined;
  }

  notesText(slidePath) {
    const path = this.notesPath(slidePath);
    if (!path) return undefined;
    const sp = this.notesBody(path);
    const body = sp && x.child(sp, "p:txBody");
    if (!body) return undefined;
    const paragraphs = x.children(body, "a:p");
    const text = paragraphs.map((p) => paragraphText(p)).join("\n").trim();
    return text ? { text, paragraphs: paragraphs.map(clone) } : undefined;
  }

  ensureNotesMaster() {
    const existing = this.relTarget(this.presPath, T.REL_TYPES.notesMaster);
    if (existing) return existing;
    const path = this.freePart("ppt/notesMasters", "notesMaster", "xml");
    this.pkg.setXml(path, partXml(T.NOTES_MASTER));
    this.pkg.setOverride(path, T.CONTENT_TYPES.notesMaster);
    const theme = this.freePart("ppt/theme", "theme", "xml");
    const masterTheme = this.relTarget(this.masters()[0], T.REL_TYPES.theme);
    this.pkg.setXml(theme, masterTheme ? clone(this.pkg.xml(masterTheme)) : partXml(T.THEME));
    this.pkg.setOverride(theme, T.CONTENT_TYPES.theme);
    this.pkg.addRel(path, T.REL_TYPES.theme, relativeTarget(path, theme));
    const rid = this.pkg.addRel(this.presPath, T.REL_TYPES.notesMaster, relativeTarget(this.presPath, path));
    const root = this.presRoot();
    x.removeChildren(root, "p:notesMasterIdLst");
    x.insertOrdered(root, x.el("p:notesMasterIdLst", null, [x.el("p:notesMasterId", { "r:id": rid })]), ["p:sldMasterIdLst", "p:notesMasterIdLst", "p:handoutMasterIdLst", "p:sldIdLst", "p:sldSz", "p:notesSz"]);
    return path;
  }

  setNotes(slidePath, paragraphs) {
    let path = this.notesPath(slidePath);
    if (!paragraphs || !paragraphs.length) {
      if (path) {
        const rel = relsOf(this.pkg, slidePath).find((entry) => entry.type === T.REL_TYPES.notesSlide);
        this.pkg.removeRel(slidePath, rel.id);
        this.collectGarbage();
      }
      return;
    }
    if (!path) {
      const master = this.ensureNotesMaster();
      path = this.freePart("ppt/notesSlides", "notesSlide", "xml");
      this.pkg.setXml(path, partXml(T.notesSlideXml('<a:p><a:endParaRPr lang="en-US"/></a:p>')));
      this.pkg.setOverride(path, T.CONTENT_TYPES.notesSlide);
      this.pkg.addRel(path, T.REL_TYPES.notesMaster, relativeTarget(path, master));
      this.pkg.addRel(path, T.REL_TYPES.slide, relativeTarget(path, slidePath));
      this.pkg.addRel(slidePath, T.REL_TYPES.notesSlide, relativeTarget(slidePath, path));
    }
    const sp = this.notesBody(path);
    if (!sp) throw new Error("the notes page has no notes placeholder");
    let body = x.child(sp, "p:txBody");
    if (!body) body = x.insertOrdered(sp, x.el("p:txBody", null, [x.el("a:bodyPr"), x.el("a:lstStyle")]), ["p:nvSpPr", "p:spPr", "p:style", "p:txBody"]);
    x.removeChildren(body, "a:p");
    x.kids(body).push(...paragraphs);
  }

  addMedia(bytes, ext) {
    const hash = crypto.createHash("sha256").update(bytes).digest("hex");
    this.mediaIndex ??= new Map();
    if (!this.mediaIndex.size) {
      for (const [path, data] of this.pkg.files) {
        if (path.startsWith("ppt/media/") && data.length) this.mediaIndex.set(crypto.createHash("sha256").update(data).digest("hex"), path);
      }
    }
    const found = this.mediaIndex.get(hash);
    if (found && this.pkg.files.has(found)) return found;
    const path = this.freePart("ppt/media", "image", ext);
    this.pkg.files.set(path, bytes);
    this.ensureDefault(ext, T.IMAGE_TYPES[ext === "jpg" ? "jpeg" : ext]);
    this.mediaIndex.set(hash, path);
    return path;
  }

  relate(part, type, target) {
    return this.pkg.addRel(part, type, type === T.REL_TYPES.hyperlink ? target : relativeTarget(part, target));
  }

  linkRel(part, url) {
    const path = `${part.slice(0, part.lastIndexOf("/") + 1)}_rels/${part.split("/").pop()}.rels`;
    const id = this.pkg.addRel(part, T.REL_TYPES.hyperlink, url);
    const node = x.children(x.rootOf(this.pkg.xml(path)), "Relationship").find((entry) => x.attrs(entry).Id === id);
    x.attrs(node).TargetMode = "External";
    return id;
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

  bytes() {
    return this.pkg.bytes();
  }
}

function paragraphText(p) {
  let out = "";
  for (const node of x.kids(p)) {
    const name = x.tagOf(node);
    if (name === "a:r" || name === "a:fld") out += x.textOf(x.child(node, "a:t"));
    else if (name === "a:br") out += "\n";
  }
  return out;
}

module.exports = { Deck, clone, numberOf, paragraphText, relativeTarget, relsOf, walk };
