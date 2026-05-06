"use client";

import Link from "next/link";

const MARKETING_URL = "https://getneko.app";

export type AppHeaderProps = {
  /** Optional back link destination (e.g. "/" or "/settings") */
  back?: { href: string; label: string };
  /** Left-side slot — used by the dashboard for role pills + date */
  children?: React.ReactNode;
};

export default function AppHeader({ back, children }: AppHeaderProps) {
  return (
    <header className="app-header">
      <div className="app-header-left">
        {back && (
          <Link className="settings-backlink" href={back.href}>
            <span aria-hidden="true" className="settings-backlink-arrow">←</span>
            <span>{back.label}</span>
          </Link>
        )}
        {children}
      </div>

      <a
        href={MARKETING_URL}
        target="_blank"
        rel="noreferrer"
        className="brand"
        style={{ textDecoration: "none" }}
        aria-label="OpenNeko — open marketing site in a new tab"
      >
        <img className="brand-icon" src="/cat.png" alt="" width={24} height={24} />
        <span className="brand-name">OpenNeko</span>
      </a>
    </header>
  );
}
