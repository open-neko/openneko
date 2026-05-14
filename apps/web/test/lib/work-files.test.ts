import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import {
  ALLOWED_UPLOAD_EXTENSIONS,
  MAX_UPLOAD_SIZE,
  joinMessageWithAttachments,
  readWorkFile,
  safeFileName,
  saveWorkUpload,
} from "@/lib/work-files";

const cleanupPaths: string[] = [];

afterEach(async () => {
  while (cleanupPaths.length > 0) {
    const path = cleanupPaths.pop();
    if (path) await rm(path, { recursive: true, force: true });
  }
  delete process.env.HOME;
});

describe("safeFileName", () => {
  it("replaces unsafe characters with underscores", () => {
    expect(safeFileName("hello world.csv")).toBe("hello_world.csv");
    // Directory components are stripped (no traversal), then the basename
    // has unsafe characters replaced.
    expect(safeFileName("rep@rt 2025.pdf")).toBe("rep_rt_2025.pdf");
  });

  it("collapses leading dots so dotfiles can't be smuggled in", () => {
    expect(safeFileName(".env")).toBe("_env");
    expect(safeFileName("..hidden.json")).toBe("_hidden.json");
  });

  it("strips directory traversal segments", () => {
    expect(safeFileName("../../etc/passwd")).not.toContain("/");
    expect(safeFileName("foo\\bar.txt")).toBe("bar.txt");
  });

  it("falls back when nothing survives sanitisation", () => {
    expect(safeFileName("")).toBe("upload.bin");
  });
});

describe("joinMessageWithAttachments", () => {
  it("returns the trimmed text unchanged when there are no attachments", () => {
    expect(joinMessageWithAttachments("  what's revenue?  ", [])).toBe("what's revenue?");
    expect(joinMessageWithAttachments("", [])).toBe("");
  });

  it("emits a clean one-line-per-file block with size in KB", () => {
    const message = joinMessageWithAttachments("Summarise this.", [
      { relativePath: "uploads/t1/q1.csv", name: "q1.csv", size: 4096 },
      { relativePath: "uploads/t1/notes.md", name: "notes.md", size: 1500 },
    ]);
    expect(message).toBe(
      [
        "Summarise this.",
        "",
        "I've attached files:",
        "- uploads/t1/q1.csv  (q1.csv, 4 KB)",
        "- uploads/t1/notes.md  (notes.md, 1 KB)",
      ].join("\n"),
    );
  });

  it("uses singular phrasing for a single attachment", () => {
    const message = joinMessageWithAttachments("", [
      { relativePath: "uploads/t1/q1.csv", name: "q1.csv", size: 2048 },
    ]);
    expect(message.startsWith("I've attached a file:")).toBe(true);
    // No leading blank line when the user typed no message.
    expect(message.startsWith("\n")).toBe(false);
  });

  it("omits the KB suffix when size is unknown", () => {
    const message = joinMessageWithAttachments("hi", [
      { relativePath: "uploads/t1/x.csv", name: "x.csv" },
    ]);
    expect(message).toContain("- uploads/t1/x.csv  (x.csv)");
  });

  it("never leaks absolute paths", () => {
    const message = joinMessageWithAttachments("hi", [
      { relativePath: "uploads/t1/q1.csv", name: "q1.csv", size: 1024 },
    ]);
    expect(message).not.toContain("/Users/");
    expect(message).not.toContain("/home/");
  });
});

describe("upload constants", () => {
  it("caps uploads at 10 MB", () => {
    expect(MAX_UPLOAD_SIZE).toBe(10 * 1024 * 1024);
  });

  it("allows the standard document extensions", () => {
    for (const ext of [".csv", ".pdf", ".md", ".txt", ".docx", ".xlsx", ".pptx", ".json", ".tsv", ".html"]) {
      expect(ALLOWED_UPLOAD_EXTENSIONS.has(ext)).toBe(true);
    }
  });

  it("rejects images and other unsupported types", () => {
    for (const ext of [".png", ".jpg", ".gif", ".exe", ".zip"]) {
      expect(ALLOWED_UPLOAD_EXTENSIONS.has(ext)).toBe(false);
    }
  });
});

describe("saveWorkUpload + readWorkFile round-trip", () => {
  it("writes the file under uploads/<threadId>/ and reads it back", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-files-"));
    cleanupPaths.push(home);
    process.env.HOME = home;

    const payload = Buffer.from("col1,col2\n1,2\n3,4\n");
    const file = new File([payload], "report.csv", { type: "text/csv" });

    const saved = await saveWorkUpload("org-test", "thread-7", file);
    expect(saved.name).toBe("report.csv");
    expect(saved.relativePath).toBe(join("uploads", "thread-7", "report.csv"));
    expect(saved.size).toBe(payload.byteLength);
    expect(saved.absolutePath).toContain("thread-7");

    const onDisk = await readFile(saved.absolutePath);
    expect(onDisk.toString()).toBe(payload.toString());

    const served = await readWorkFile("org-test", "uploads/thread-7/report.csv");
    expect(served.filename).toBe("report.csv");
    expect(served.mimeType.startsWith("text/csv")).toBe(true);
    expect(served.data.toString()).toBe(payload.toString());
  }, 30_000);

  it("sanitises filenames and refuses path escapes when reading", async () => {
    const home = await mkdtemp(join(tmpdir(), "neko-work-files-"));
    cleanupPaths.push(home);
    process.env.HOME = home;

    const file = new File(["x"], "../../etc/passwd", { type: "text/plain" });
    const saved = await saveWorkUpload("org-test", "thread-x", file);
    expect(saved.name).not.toContain("/");
    expect(saved.name).not.toContain("..");

    await expect(readWorkFile("org-test", "secrets/foo.txt")).rejects.toThrow();
  }, 30_000);
});
