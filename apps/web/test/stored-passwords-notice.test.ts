import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { StoredPasswordsLine, savedBeforeUpgrade } from "../src/app/admin/StoredPasswordsNotice";

it("names each stored password that has no database or login", () => {
  expect(savedBeforeUpgrade([
    { name: "erp", dbname: null, login: null },
    { name: "crm", dbname: "crm", login: "reader" },
    { name: "wms", dbname: "wms", login: null },
  ])).toEqual(["erp", "wms"]);
});

it("says once that OpenNeko waits until an admin saves the password again", () => {
  const html = renderToStaticMarkup(createElement(StoredPasswordsLine, { names: ["erp"] }));
  expect(html).toContain("The stored source password erp was saved before this release with no database and login.");
  expect(html).toContain("OpenNeko does not send it until an admin saves it again at");
  expect(html).toContain('href="/admin/settings/graphjin"');
  expect(renderToStaticMarkup(createElement(StoredPasswordsLine, { names: [] }))).toBe("");
});
