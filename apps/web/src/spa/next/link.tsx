import { forwardRef, type AnchorHTMLAttributes, type MouseEvent } from "react";
import { Link as RouterLink } from "react-router";

type Href = string | { pathname?: string | null; query?: Record<string, string | number | boolean | undefined>; hash?: string | null };

export type LinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & {
  href: Href;
  replace?: boolean;
  scroll?: boolean;
  prefetch?: boolean | null;
  onNavigate?: (event: { preventDefault(): void }) => void;
};

export function hrefToString(href: Href): string {
  if (typeof href === "string") return href;
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(href.query ?? {})) if (value !== undefined) query.set(key, String(value));
  const search = query.toString();
  const hash = href.hash ? (href.hash.startsWith("#") ? href.hash : `#${href.hash}`) : "";
  return `${href.pathname ?? ""}${search ? `?${search}` : ""}${hash}`;
}

function isDocumentHref(href: string): boolean {
  return /^[a-z][a-z0-9+.-]*:/i.test(href) || href.startsWith("//") || /^\/api(?:\/|$)/.test(href);
}

const Link = forwardRef<HTMLAnchorElement, LinkProps>(function Link(
  { href, replace, scroll, prefetch, onNavigate, onClick, ...rest },
  ref,
) {
  const to = hrefToString(href);
  if (isDocumentHref(to)) return <a ref={ref} href={to} onClick={onClick} {...rest} />;
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(event);
    if (event.defaultPrevented || !onNavigate) return;
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if (rest.target && rest.target !== "_self") return;
    onNavigate({ preventDefault: () => event.preventDefault() });
  };
  return <RouterLink ref={ref} to={to} replace={replace} preventScrollReset={scroll === false} prefetch={prefetch === false ? "none" : undefined} onClick={handleClick} {...rest} />;
});

export default Link;
