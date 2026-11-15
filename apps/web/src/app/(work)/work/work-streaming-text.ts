export type HeldBlock = "table" | "code" | "card";

const FENCE_LINE = /^ {0,3}(`{3,}|~{3,})(.*)$/;
const PART_FENCE_LINE = /^ {0,3}(`{1,2}|~{1,2})$/;
const TABLE_LINE = /^ {0,3}\|/;

function hiddenFenceInfo(info: string, lastLine: boolean): boolean {
  return info.startsWith("neko_") || (lastLine && "neko_".startsWith(info));
}

/**
 * Splits streamed answer text into the part that is safe to render now and a
 * block still being written: an open code fence, or a table with no line
 * after it yet. `held` names the block for its placeholder; null means the
 * held tail is a fence marker or a hidden fence, which needs no placeholder.
 */
export function holdOpenBlock(text: string): { shown: string; held: HeldBlock | null } {
  const lines = text.split("\n");
  const starts: number[] = [];
  let offset = 0;
  for (const line of lines) {
    starts.push(offset);
    offset += line.length + 1;
  }

  let open: { at: number; mark: string; info: string; lastLine: boolean } | null = null;
  lines.forEach((line, i) => {
    const fence = FENCE_LINE.exec(line);
    if (!fence) return;
    const mark = fence[1];
    const info = fence[2].trim();
    if (!open) {
      open = { at: starts[i], mark, info, lastLine: i === lines.length - 1 };
    } else if (mark[0] === open.mark[0] && mark.length >= open.mark.length && info === "") {
      open = null;
    }
  });
  if (open) {
    const { at, info, lastLine } = open;
    return { shown: text.slice(0, at), held: hiddenFenceInfo(info, lastLine) ? null : "code" };
  }

  const last = lines.length - 1;
  if (PART_FENCE_LINE.test(lines[last])) {
    return { shown: text.slice(0, starts[last]), held: null };
  }

  const end = lines[last] === "" ? last - 1 : last;
  let first = end;
  while (first >= 0 && TABLE_LINE.test(lines[first])) first--;
  if (first < end) {
    return { shown: text.slice(0, starts[first + 1]), held: "table" };
  }
  return { shown: text, held: null };
}
