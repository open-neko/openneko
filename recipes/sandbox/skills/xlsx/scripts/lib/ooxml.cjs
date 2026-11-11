"use strict";

const { unzipSync, zipSync, strFromU8, strToU8 } = require("fflate");
const { XMLParser, XMLBuilder } = require("fast-xml-parser");

const MAX_UNZIPPED_BYTES = 512 * 1024 * 1024;

const parser = new XMLParser({
  preserveOrder: true,
  ignoreAttributes: false,
  attributeNamePrefix: "",
  parseTagValue: false,
  parseAttributeValue: false,
  trimValues: false,
  processEntities: true,
  htmlEntities: false,
});

const builder = new XMLBuilder({
  preserveOrder: true,
  ignoreAttributes: false,
  attributeNamePrefix: "",
  suppressEmptyNode: true,
  processEntities: true,
});

function tagOf(node) {
  for (const key of Object.keys(node)) if (key !== ":@") return key;
  return undefined;
}

function kids(node) {
  return node[tagOf(node)];
}

function attrs(node) {
  if (!node[":@"]) node[":@"] = {};
  return node[":@"];
}

function el(name, attributes, children) {
  const node = { [name]: children ?? [] };
  if (attributes) {
    const clean = {};
    for (const [key, value] of Object.entries(attributes)) if (value !== undefined && value !== null) clean[key] = String(value);
    if (Object.keys(clean).length) node[":@"] = clean;
  }
  return node;
}

function textNode(value) {
  return { "#text": String(value) };
}

function child(node, name) {
  return kids(node).find((entry) => tagOf(entry) === name);
}

function children(node, name) {
  return kids(node).filter((entry) => tagOf(entry) === name);
}

function textOf(node) {
  if (!node) return "";
  let out = "";
  for (const entry of kids(node)) {
    if ("#text" in entry) out += entry["#text"];
    else out += textOf(entry);
  }
  return out;
}

function removeChildren(node, name) {
  const list = kids(node);
  for (let i = list.length - 1; i >= 0; i--) if (tagOf(list[i]) === name) list.splice(i, 1);
}

function insertOrdered(parent, node, order) {
  const list = kids(parent);
  const rank = order.indexOf(tagOf(node));
  let at = list.length;
  for (let i = 0; i < list.length; i++) {
    const other = order.indexOf(tagOf(list[i]));
    if (other > rank) {
      at = i;
      break;
    }
  }
  list.splice(at, 0, node);
  return node;
}

function ensureChild(parent, name, order) {
  return child(parent, name) ?? insertOrdered(parent, el(name), order);
}

function parseXml(text) {
  return parser.parse(text);
}

function buildXml(tree) {
  const body = builder.build(tree.filter((node) => tagOf(node) !== "?xml"));
  return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n${body}`;
}

function rootOf(tree) {
  return tree.find((node) => {
    const name = tagOf(node);
    return name && name !== "?xml" && name !== "#text" && name !== "#comment";
  });
}

function joinPart(base, target) {
  if (target.startsWith("/")) return target.slice(1);
  const parts = base.split("/").slice(0, -1);
  for (const piece of target.split("/")) {
    if (piece === "..") parts.pop();
    else if (piece !== ".") parts.push(piece);
  }
  return parts.join("/");
}

function relsPathFor(part) {
  const slash = part.lastIndexOf("/");
  return `${part.slice(0, slash + 1)}_rels/${part.slice(slash + 1)}.rels`;
}

class Package {
  constructor(files) {
    this.files = files;
    this.trees = new Map();
  }

  static open(bytes) {
    let total = 0;
    const files = unzipSync(bytes, {
      filter(file) {
        total += file.originalSize;
        if (total > MAX_UNZIPPED_BYTES) throw new Error("the file unpacks to more than 512 MB");
        return true;
      },
    });
    if (!files["[Content_Types].xml"]) throw new Error("not an Office Open XML file: [Content_Types].xml is missing");
    return new Package(new Map(Object.entries(files)));
  }

  xml(path) {
    if (!this.trees.has(path)) {
      const bytes = this.files.get(path);
      if (!bytes) return undefined;
      this.trees.set(path, parseXml(strFromU8(bytes)));
    }
    return this.trees.get(path);
  }

  setXml(path, tree) {
    this.trees.set(path, tree);
    this.files.set(path, new Uint8Array());
  }

  remove(path) {
    this.trees.delete(path);
    this.files.delete(path);
  }

  rels(part) {
    const tree = this.xml(relsPathFor(part));
    if (!tree) return [];
    return children(rootOf(tree), "Relationship").map((node) => {
      const a = attrs(node);
      return { id: a.Id, type: a.Type, target: a.Target, mode: a.TargetMode, part: a.TargetMode === "External" ? undefined : joinPart(part, a.Target), node };
    });
  }

  addRel(part, type, target) {
    const path = relsPathFor(part);
    let tree = this.xml(path);
    if (!tree) {
      tree = [el("Relationships", { xmlns: "http://schemas.openxmlformats.org/package/2006/relationships" })];
      this.setXml(path, tree);
    }
    const root = rootOf(tree);
    const used = new Set(children(root, "Relationship").map((node) => attrs(node).Id));
    let n = used.size + 1;
    while (used.has(`rId${n}`)) n++;
    const id = `rId${n}`;
    kids(root).push(el("Relationship", { Id: id, Type: type, Target: target }));
    return id;
  }

  removeRel(part, id) {
    const tree = this.xml(relsPathFor(part));
    if (!tree) return;
    const list = kids(rootOf(tree));
    const at = list.findIndex((node) => tagOf(node) === "Relationship" && attrs(node).Id === id);
    if (at >= 0) list.splice(at, 1);
  }

  setOverride(part, contentType) {
    const root = rootOf(this.xml("[Content_Types].xml"));
    const name = `/${part}`;
    const existing = children(root, "Override").find((node) => attrs(node).PartName === name);
    if (contentType === null) {
      if (existing) kids(root).splice(kids(root).indexOf(existing), 1);
      return;
    }
    if (existing) attrs(existing).ContentType = contentType;
    else kids(root).push(el("Override", { PartName: name, ContentType: contentType }));
  }

  bytes() {
    const out = {};
    for (const [path, data] of this.files) {
      out[path] = this.trees.has(path) ? strToU8(buildXml(this.trees.get(path))) : data;
    }
    return zipSync(out, { level: 6 });
  }
}

module.exports = {
  Package,
  attrs,
  buildXml,
  child,
  children,
  el,
  ensureChild,
  insertOrdered,
  joinPart,
  kids,
  parseXml,
  removeChildren,
  rootOf,
  tagOf,
  textNode,
  textOf,
};
