/**
 * Markdown an agent wrote, rendered for reading.
 *
 * A model's answer is untrusted: it may repeat whatever a tool or a stranger
 * put in front of it. So raw HTML is never parsed (there is no rehype-raw,
 * and react-markdown escapes HTML it meets as text), links go only to http
 * and https and open away from the dashboard, and images are never fetched —
 * an image is shown as a link to where it points, so a model cannot make the
 * browser call out by writing one.
 */
import type { ComponentProps } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import { cn } from "@/lib/utils";

/** The URL if it is http or https, absolute; anything else is not a link. */
export function safeHref(url: string | undefined | null): string | null {
  if (!url) return null;
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : null;
  } catch {
    return null;
  }
}

/** For react-markdown: every URL it emits passes through here first. */
export function urlTransform(url: string): string {
  return safeHref(url) ?? "";
}

/**
 * A link that goes only where safeHref allows, and away from the dashboard.
 * A second check behind urlTransform, so neither alone is the only guard.
 */
export function Anchor({ href, children }: ComponentProps<"a">) {
  const safe = safeHref(href);
  if (!safe) return <span>{children}</span>;
  return (
    <a
      href={safe}
      target="_blank"
      rel="noopener noreferrer nofollow"
      className="text-primary underline underline-offset-2 hover:no-underline"
    >
      {children}
    </a>
  );
}

const components: Components = {
  a: Anchor,
  img: ({ src, alt }) => {
    const label = alt || "image";
    const safe = safeHref(typeof src === "string" ? src : null);
    return safe ? <Anchor href={safe}>{label}</Anchor> : <span>{label}</span>;
  },
  h1: ({ children }) => <h3 className="mt-4 mb-2 text-base font-semibold">{children}</h3>,
  h2: ({ children }) => <h3 className="mt-4 mb-2 text-sm font-semibold">{children}</h3>,
  h3: ({ children }) => <h4 className="mt-3 mb-1.5 text-sm font-semibold">{children}</h4>,
  h4: ({ children }) => <h5 className="mt-3 mb-1 text-sm font-medium">{children}</h5>,
  h5: ({ children }) => <h6 className="mt-3 mb-1 text-sm font-medium">{children}</h6>,
  h6: ({ children }) => <h6 className="mt-3 mb-1 text-sm font-medium">{children}</h6>,
  p: ({ children }) => <p className="my-2 leading-relaxed">{children}</p>,
  ul: ({ children }) => <ul className="my-2 list-disc space-y-1 pl-5">{children}</ul>,
  ol: ({ children }) => <ol className="my-2 list-decimal space-y-1 pl-5">{children}</ol>,
  li: ({ children }) => <li className="pl-0.5">{children}</li>,
  blockquote: ({ children }) => (
    <blockquote className="my-2 border-l-2 border-border-strong pl-3 text-muted-foreground">
      {children}
    </blockquote>
  ),
  hr: () => <hr className="my-4 border-border" />,
  pre: ({ children }) => (
    <pre className="my-2 overflow-x-auto rounded-md border border-border bg-surface p-3 font-mono text-xs leading-relaxed [&>code]:bg-transparent [&>code]:p-0">
      {children}
    </pre>
  ),
  code: ({ children, className }) => (
    <code className={cn("rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]", className)}>
      {children}
    </code>
  ),
  table: ({ children }) => (
    <div className="my-3 overflow-x-auto rounded-md border border-border">
      <table className="w-full border-collapse text-left text-xs">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-surface">{children}</thead>,
  th: ({ children }) => (
    <th className="border-b border-border px-3 py-1.5 font-semibold">{children}</th>
  ),
  td: ({ children }) => (
    <td className="border-b border-border px-3 py-1.5 align-top tabular-nums">{children}</td>
  ),
};

export function Markdown({ children, className }: { children: string; className?: string }) {
  return (
    <div
      className={cn("text-sm break-words [&>:first-child]:mt-0 [&>:last-child]:mb-0", className)}
    >
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        skipHtml
        urlTransform={urlTransform}
        components={components}
      >
        {children}
      </ReactMarkdown>
    </div>
  );
}
