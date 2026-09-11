"use client";

import { useEffect, useState } from "react";

type LocalDateTimeProps = {
  value: string | Date;
  fallback?: string;
  className?: string;
};

export function LocalDateTime({
  value,
  fallback = "—",
  className,
}: LocalDateTimeProps) {
  const iso = value instanceof Date ? value.toISOString() : value;
  const [formatted, setFormatted] = useState<string | null>(null);

  useEffect(() => {
    const id = window.setTimeout(() => {
      setFormatted(
        new Intl.DateTimeFormat(undefined, {
          dateStyle: "medium",
          timeStyle: "short",
        }).format(new Date(iso)),
      );
    }, 0);
    return () => window.clearTimeout(id);
  }, [iso]);

  return (
    <time dateTime={iso} className={className}>
      {formatted ?? fallback}
    </time>
  );
}
