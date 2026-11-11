"use strict";

const x = require("./ooxml.cjs");

const PROPERTY_CHANGES = {
  "w:rPrChange": { owner: "w:rPr", keep: [] },
  "w:pPrChange": { owner: "w:pPr", keep: ["w:rPr", "w:sectPr"] },
  "w:sectPrChange": { owner: "w:sectPr", keep: ["w:headerReference", "w:footerReference"] },
  "w:tblPrChange": { owner: "w:tblPr", keep: [] },
  "w:trPrChange": { owner: "w:trPr", keep: ["w:ins", "w:del"] },
  "w:tcPrChange": { owner: "w:tcPr", keep: [] },
  "w:tblPrExChange": { owner: "w:tblPrEx", keep: [] },
  "w:tblGridChange": { owner: "w:tblGrid", keep: [] },
};
const RANGE_MARKS = new Set(["w:moveFromRangeStart", "w:moveFromRangeEnd", "w:moveToRangeStart", "w:moveToRangeEnd"]);

function matcher(filter) {
  const ids = filter.ids ? new Set(filter.ids.map(String)) : undefined;
  const author = filter.author ? String(filter.author).toLowerCase() : undefined;
  return (node) => {
    const a = node[":@"] ?? {};
    if (ids && !ids.has(String(a["w:id"]))) return false;
    if (author && String(a["w:author"] ?? "").toLowerCase() !== author) return false;
    return true;
  };
}

function renameDeleted(node) {
  if (!node || typeof node !== "object" || "#text" in node) return;
  let tag = x.tagOf(node);
  if (!tag) return;
  const map = { "w:delText": "w:t", "w:delInstrText": "w:instrText" };
  if (map[tag]) {
    node[map[tag]] = node[tag];
    delete node[tag];
    tag = map[tag];
  }
  if (Array.isArray(node[tag])) node[tag].forEach(renameDeleted);
}

function revertProperties(parent, change) {
  const spec = PROPERTY_CHANGES[x.tagOf(change)];
  const old = x.kids(change).find((entry) => x.tagOf(entry) && !("#text" in entry));
  const kept = x.kids(parent).filter((entry) => spec.keep.includes(x.tagOf(entry)));
  const restored = old ? x.kids(old).filter((entry) => !spec.keep.includes(x.tagOf(entry))) : [];
  const list = x.kids(parent);
  list.length = 0;
  if (x.tagOf(parent) === "w:pPr") list.push(...restored, ...kept);
  else list.push(...kept, ...restored);
}

function process(node, accept, match, counts, marks) {
  const list = x.kids(node);
  if (!Array.isArray(list)) return;
  for (let i = 0; i < list.length; i++) {
    const entry = list[i];
    const tag = x.tagOf(entry);
    if (!tag || "#text" in entry) continue;
    const isInsert = tag === "w:ins" || tag === "w:moveTo";
    const isDelete = tag === "w:del" || tag === "w:moveFrom";
    const parentTag = x.tagOf(node);
    if ((isInsert || isDelete) && (parentTag === "w:rPr" || parentTag === "w:trPr")) {
      if (match(entry)) marks.push({ owner: node, entry, isInsert });
      continue;
    }
    if ((isInsert || isDelete) && match(entry)) {
      counts.changes++;
      const keepContent = accept ? isInsert : isDelete;
      if (keepContent) {
        const inner = x.kids(entry);
        if (!accept) inner.forEach(renameDeleted);
        list.splice(i, 1, ...inner);
      } else list.splice(i, 1);
      i--;
      continue;
    }
    if (RANGE_MARKS.has(tag) && match(entry)) {
      list.splice(i, 1);
      i--;
      continue;
    }
    if (PROPERTY_CHANGES[tag] && match(entry)) {
      counts.changes++;
      if (accept) {
        list.splice(i, 1);
        i--;
      } else {
        revertProperties(node, entry);
        i = -1;
      }
      continue;
    }
    process(entry, accept, match, counts, marks);
  }
}

function mergeWithNext(p, parentList) {
  const at = parentList.indexOf(p);
  const next = parentList[at + 1];
  if (!next || x.tagOf(next) !== "w:p") return false;
  const moving = x.kids(p).filter((entry) => x.tagOf(entry) !== "w:pPr");
  const target = x.kids(next);
  const pPrAt = target.findIndex((entry) => x.tagOf(entry) === "w:pPr");
  target.splice(pPrAt + 1, 0, ...moving);
  parentList.splice(at, 1);
  return true;
}

function findParentList(root, target) {
  let found;
  const visit = (node) => {
    if (found) return;
    const list = x.kids(node);
    if (!Array.isArray(list)) return;
    if (list.includes(target)) {
      found = list;
      return;
    }
    for (const entry of list) if (entry && typeof entry === "object" && !("#text" in entry) && x.tagOf(entry)) visit(entry);
  };
  visit(root);
  return found;
}

function findOwner(root, rPrOrTrPr) {
  let found;
  const visit = (node) => {
    if (found) return;
    const list = x.kids(node);
    if (!Array.isArray(list)) return;
    for (const entry of list) {
      if (!entry || typeof entry !== "object" || "#text" in entry || !x.tagOf(entry)) continue;
      if (x.tagOf(entry) === "w:p") {
        const pPr = x.child(entry, "w:pPr");
        if (pPr && x.child(pPr, "w:rPr") === rPrOrTrPr) {
          found = entry;
          return;
        }
      }
      if (x.tagOf(entry) === "w:tr" && x.child(entry, "w:trPr") === rPrOrTrPr) {
        found = entry;
        return;
      }
      visit(entry);
    }
  };
  visit(root);
  return found;
}

function applyMarks(root, accept, marks, counts) {
  for (const { owner, entry, isInsert } of marks.reverse()) {
    const ownerList = x.kids(owner);
    const at = ownerList.indexOf(entry);
    if (at < 0) continue;
    counts.changes++;
    const removeThing = accept ? !isInsert : isInsert;
    ownerList.splice(at, 1);
    if (!removeThing) continue;
    const thing = findOwner(root, owner);
    if (!thing) continue;
    const parentList = findParentList(root, thing);
    if (!parentList) continue;
    if (x.tagOf(thing) === "w:tr") parentList.splice(parentList.indexOf(thing), 1);
    else if (!mergeWithNext(thing, parentList)) {
      const empty = !x.kids(thing).some((node) => x.tagOf(node) !== "w:pPr");
      const others = parentList.filter((node) => node !== thing && (x.tagOf(node) === "w:p" || x.tagOf(node) === "w:tbl")).length;
      if (empty && others > 0) parentList.splice(parentList.indexOf(thing), 1);
    }
  }
}

function removeEmptyTables(root) {
  const visit = (node) => {
    const list = x.kids(node);
    if (!Array.isArray(list)) return;
    for (let i = list.length - 1; i >= 0; i--) {
      const entry = list[i];
      if (!entry || typeof entry !== "object" || "#text" in entry || !x.tagOf(entry)) continue;
      if (x.tagOf(entry) === "w:tbl" && !x.children(entry, "w:tr").length) list.splice(i, 1);
      else visit(entry);
    }
  };
  visit(root);
}

function resolve(doc, accept, filter = {}) {
  const match = matcher(filter);
  const counts = { changes: 0 };
  for (const tree of doc.allTrees()) {
    const root = x.rootOf(tree);
    if (!root) continue;
    const marks = [];
    process(root, accept, match, counts, marks);
    applyMarks(root, accept, marks, counts);
    removeEmptyTables(root);
  }
  return counts.changes;
}

module.exports = { resolve };
