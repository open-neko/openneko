"use client";

import Link from "next/link";

export default function SectionNav({
  current,
  children,
}: {
  current: "dashboard" | "work" | "settings" | "business-profile";
  children?: React.ReactNode;
}) {
  return (
    <div className="dash-meta">
      <Link
        href="/"
        className={`settings-link nav-link${current === "dashboard" ? " is-active" : ""}`}
      >
        Dashboard
      </Link>
      <Link
        href="/work"
        className={`settings-link nav-link${current === "work" ? " is-active" : ""}`}
      >
        Work
      </Link>
      <Link
        href="/business-profile"
        className={`settings-link nav-link${current === "business-profile" ? " is-active" : ""}`}
      >
        Business Profile
      </Link>
      <Link
        href="/settings"
        className={`settings-link nav-link${current === "settings" ? " is-active" : ""}`}
      >
        Settings
      </Link>
      {children}
    </div>
  );
}
