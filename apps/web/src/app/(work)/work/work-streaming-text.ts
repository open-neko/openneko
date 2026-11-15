import remarkGfm from "remark-gfm";
import remarkParse from "remark-parse";
import { unified } from "unified";

export type HeldBlock = "table" | "code" | "card";

type MdNode = {
  type: string;
  lang?: string | null;
  children?: MdNode[];
  position?: { start: { offset?: number }; end: { offset?: number } };
};

const parser = unified().use(remarkParse).use(remarkGfm);

const CONTAINERS = new Set(["root", "blockquote", "list", "listItem", "footnoteDefinition"]);
const OPEN_FENCE = /^(`{3,}|~{3,})(.*)$/;
const PART_FENCE = /^(`{1,2}|~{1,2})$/;
const TABLE_DELIMITER = /^[\s|:-]*-[\s|:-]*$/;
const OPEN_LINK = /!?\[[^[\]\n]*(?:\](?:\([^)\n]*)?)?$/;

function bare(line: string): string {
  return line.replace(/^(?:[ \t]*>)*[ \t]*/, "");
}

function lineStart(text: string, at: number): number {
  return text.lastIndexOf("\n", at - 1) + 1;
}

function lastLeaf(node: MdNode): MdNode {
  while (CONTAINERS.has(node.type) && node.children?.length) {
    node = node.children[node.children.length - 1];
  }
  return node;
}

function textNodeAt(node: MdNode, at: number): boolean {
  const start = node.position?.start.offset ?? -1;
  const end = node.position?.end.offset ?? -1;
  if (at < start || at >= end) return false;
  if (node.type === "text") return true;
  return node.children?.some((child) => textNodeAt(child, at)) ?? false;
}

function openFence(text: string, start: number, end: number): { info: string } | null {
  const lines = text.slice(start, end).split("\n").map(bare);
  const open = OPEN_FENCE.exec(lines[0]);
  if (!open) return null;
  const mark = open[1];
  const close = lines.length > 1 ? /^(`+|~+)[ \t]*$/.exec(lines[lines.length - 1]) : null;
  if (close && close[1][0] === mark[0] && close[1].length >= mark.length) return null;
  return { info: open[2].trim() };
}

/**
 * Splits streamed answer text into the part that is safe to render now and a
 * block still being written: an open code fence, a table that a later line can
 * still extend, or a link or image without its closing mark. `held` names the
 * block for its placeholder; null means the held tail needs no placeholder.
 */
export function holdOpenBlock(text: string): { shown: string; held: HeldBlock | null } {
  let base = 0;
  for (const [known, at] of lastBlockStarts) {
    if (at > base && text.startsWith(known)) base = at;
  }
  const result = holdInTail(text.slice(base), (at) => {
    lastBlockStarts.set(text, base + at);
    if (lastBlockStarts.size > 8) lastBlockStarts.delete(lastBlockStarts.keys().next().value!);
  });
  return { shown: text.slice(0, base) + result.shown, held: result.held };
}

const lastBlockStarts = new Map<string, number>();

function holdInTail(text: string, lastBlockAt: (at: number) => void): { shown: string; held: HeldBlock | null } {
  const root = parser.parse(text) as MdNode;
  const lastBlock = root.children?.[root.children.length - 1]?.position?.start.offset;
  if (lastBlock !== undefined) lastBlockAt(lineStart(text, lastBlock));
  const leaf = lastLeaf(root);
  const start = leaf.position?.start.offset;
  const end = leaf.position?.end.offset;
  if (start === undefined || end === undefined) return { shown: text, held: null };

  if (leaf.type === "code") {
    const fence = openFence(text, start, end);
    if (fence) {
      const lastLine = !text.includes("\n", start);
      const hidden = fence.info.startsWith("neko_") || (lastLine && "neko_".startsWith(fence.info));
      return { shown: text.slice(0, lineStart(text, start)), held: hidden ? null : "code" };
    }
    return { shown: text, held: null };
  }

  const lastLineAt = lineStart(text, text.length);
  if (PART_FENCE.test(bare(text.slice(lastLineAt)))) {
    return { shown: text.slice(0, lastLineAt), held: null };
  }

  const closed = /\n[ \t>]*\n/.test(text.slice(end));
  if (leaf.type === "table") {
    return closed ? { shown: text, held: null } : { shown: text.slice(0, lineStart(text, start)), held: "table" };
  }
  if (leaf.type !== "paragraph" && leaf.type !== "heading") return { shown: text, held: null };

  if (!closed) {
    const lines = text.slice(start, end).split("\n");
    let first = lines.length;
    while (first > 0) {
      const line = bare(lines[first - 1]);
      if (!line.includes("|") && !TABLE_DELIMITER.test(line)) break;
      first--;
    }
    if (lines.slice(first).some((line) => line.includes("|"))) {
      const at = start + lines.slice(0, first).reduce((sum, line) => sum + line.length + 1, 0);
      return { shown: text.slice(0, lineStart(text, at)), held: "table" };
    }
  }

  const link = OPEN_LINK.exec(text.slice(start, end));
  if (link && text.slice(end).trim() === "" && textNodeAt(leaf, start + link.index)) {
    return { shown: text.slice(0, start + link.index), held: null };
  }
  return { shown: text, held: null };
}
