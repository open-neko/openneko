import type { ComponentProps, ComponentType, ReactNode } from "react";
import { isValidElement } from "react";
import ReactMarkdown, { type Components, type ExtraProps, type Options } from "react-markdown";

export type { Components };

function base(): string {
  return typeof window === "undefined" ? "http://openneko.invalid" : window.location.origin;
}

/** The host of a link or image that leaves the install, or null for one that stays. */
export function outsideHost(url: string | undefined): string | null {
  if (!url) return null;
  let parsed: URL;
  try {
    parsed = new URL(url, base());
  } catch {
    return null;
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return null;
  return parsed.origin === base() ? null : parsed.host;
}

function textOf(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (isValidElement<{ children?: ReactNode }>(node)) return textOf(node.props.children);
  return "";
}

type AnchorProps = ComponentProps<"a"> & ExtraProps;
type ImageProps = ComponentProps<"img"> & ExtraProps;

function safeAnchor(Inner?: ComponentType<AnchorProps>) {
  return function SafeAnchor({ node, ...props }: AnchorProps) {
    void node;
    const host = outsideHost(props.href);
    if (!host) return Inner ? <Inner {...props} /> : <a {...props} />;
    const shown = textOf(props.children).toLowerCase().includes(host.toLowerCase());
    const children = shown ? props.children : (
      <>
        {props.children}
        <span className="markdown-link-host"> ({host})</span>
      </>
    );
    const out = { ...props, children, target: "_blank", rel: "noopener noreferrer", referrerPolicy: "no-referrer" as const };
    return Inner ? <Inner {...out} /> : <a {...out} />;
  };
}

/** An image from outside the install becomes a link, so the page fetches nothing that the text names. */
function SafeImage({ node, src, alt, ...props }: ImageProps) {
  void node;
  const url = typeof src === "string" ? src : undefined;
  const host = outsideHost(url);
  if (host) {
    return (
      <a href={url} target="_blank" rel="noopener noreferrer" referrerPolicy="no-referrer" className="markdown-remote-image">
        {`Image${alt ? `: ${alt}` : ""}`}
        <span className="markdown-link-host"> ({host})</span>
      </a>
    );
  }
  return <img src={url} alt={alt ?? ""} {...props} />;
}

/** react-markdown with links that name their host and no image from outside the install. */
const PlainAnchor = safeAnchor();
const wrapped = new WeakMap<ComponentType<AnchorProps>, ComponentType<AnchorProps>>();

function anchorFor(Inner?: ComponentType<AnchorProps>): ComponentType<AnchorProps> {
  if (!Inner) return PlainAnchor;
  let out = wrapped.get(Inner);
  if (!out) {
    out = safeAnchor(Inner);
    wrapped.set(Inner, out);
  }
  return out;
}

export default function SafeMarkdown({ components, ...options }: Options) {
  const merged: Components = {
    ...components,
    a: anchorFor(components?.a as ComponentType<AnchorProps> | undefined),
    img: SafeImage,
  };
  return <ReactMarkdown {...options} components={merged} />;
}
