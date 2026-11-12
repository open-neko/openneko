import { describe, expect, it } from "vitest";
import {
  ALLOWED_UPLOAD_EXTENSIONS,
  joinMessageWithAttachments,
  safeFileName,
} from "@/lib/work-files";

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
  it("allows the standard document extensions", () => {
    for (const ext of [
      ".csv",
      ".pdf",
      ".md",
      ".markdown",
      ".txt",
      ".docx",
      ".xlsx",
      ".pptx",
    ]) {
      expect(ALLOWED_UPLOAD_EXTENSIONS.has(ext)).toBe(true);
    }
  });

  it("rejects images and other unsupported types", () => {
    for (const ext of [
      ".png",
      ".jpg",
      ".gif",
      ".exe",
      ".zip",
      ".json",
      ".tsv",
      ".html",
    ]) {
      expect(ALLOWED_UPLOAD_EXTENSIONS.has(ext)).toBe(false);
    }
  });
});
