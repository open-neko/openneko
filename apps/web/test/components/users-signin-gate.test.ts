import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn(), push: vi.fn() }) }));
import { UsersClient } from "@/app/admin/users/UsersClient";

const users = [
  { id: "usr_1", email: "owner@company.com", name: "Owner", role: "admin" as const,
    source: "local", groups: [], disabled: false, hasSignedIn: false,
    lastLoginAt: null, createdAt: null },
];

describe("Users page without a sign-in plugin", () => {
  it("offers guidance instead of a form nobody can use", () => {
    const html = renderToStaticMarkup(
      createElement(UsersClient, { users, signInProvider: null }),
    );
    // The form collected a real address for an account that cannot sign in.
    expect(html).not.toContain('id="new-user-email"');
    expect(html).toContain("No sign-in plugin yet");
    expect(html).toContain("/admin/plugins");
    // The existing people stay visible.
    expect(html).toContain("owner@company.com");
  });

  it("keeps the form once a plugin is installed", () => {
    const html = renderToStaticMarkup(
      createElement(UsersClient, { users, signInProvider: "Email link" }),
    );
    expect(html).toContain('id="new-user-email"');
    expect(html).not.toContain("No sign-in plugin yet");
  });
});
