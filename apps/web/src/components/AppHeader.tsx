"use client";

import Link from "next/link";
import { ArrowLeft } from "lucide-react";

const MARKETING_URL = "https://getneko.app";

export type AppHeaderProps = {
  back?: { href: string; label: string };
  children?: React.ReactNode;
};

export default function AppHeader({ back, children }: AppHeaderProps) {
  return (
    <header className="app-header">
      <div className="app-header-left">
        {back && (
          <Link className="settings-backlink" href={back.href}>
            <ArrowLeft size={14} strokeWidth={2.25} aria-hidden="true" className="settings-backlink-arrow" />
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
        <span aria-hidden="true" className="brand-tick">·</span>
        <span aria-hidden="true" className="brand-version">v1</span>
      </a>
    </header>
  );
}
