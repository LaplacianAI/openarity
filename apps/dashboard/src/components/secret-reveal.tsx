/**
 * A webhook watcher's address and secret, shown the once the brain gives
 * them. The secret lives in the caller's state only: never the query cache,
 * storage or the address bar, and gone when the page is left.
 */
import { Check, Copy } from "lucide-react";
import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";

function CopyButton({ text, what }: { text: string; what: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 2000);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <Button
      type="button"
      size="sm"
      variant="outline"
      aria-label={copied ? `${what} copied` : `Copy the ${what}`}
      onClick={() => void navigator.clipboard?.writeText(text).then(() => setCopied(true))}
    >
      {copied ? (
        <Check
          aria-hidden
          className="size-3.5 text-succeeded animate-in fade-in-0 duration-(--dur-press)"
        />
      ) : (
        <Copy aria-hidden className="size-3.5" />
      )}
      {copied ? "Copied" : "Copy"}
    </Button>
  );
}

function Line({ label, value, what }: { label: string; value: string; what: string }) {
  return (
    <div className="space-y-1">
      <p className="text-xs text-muted-foreground">{label}</p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 break-all rounded bg-secondary px-2 py-1 font-mono text-xs">
          {value}
        </code>
        <CopyButton text={value} what={what} />
      </div>
    </div>
  );
}

export function SecretReveal({
  url,
  secret,
  onDone,
}: {
  url?: string | undefined;
  secret: string;
  onDone: () => void;
}) {
  return (
    <section
      aria-label="The watcher's secret"
      className="space-y-3 rounded-lg bg-surface px-4 py-3 duration-200 ease-out animate-in fade-in-0 motion-safe:slide-in-from-bottom-1"
    >
      <p className="text-sm font-medium">Shown this once. Copy it now: it cannot be shown again.</p>
      {url ? <Line label="Its address" value={url} what="address" /> : null}
      <Line label="Its secret" value={secret} what="secret" />
      <div className="space-y-1 text-xs text-muted-foreground">
        <p>
          On GitHub: Settings › Webhooks › Add webhook. Paste the address as the payload URL, choose
          application/json, and paste the secret.
        </p>
        <p>
          Anything else signs each request as a custom channel's webhook does: a timestamp and an
          HMAC of the body, keyed by the secret.
        </p>
      </div>
      <Button type="button" size="sm" variant="ghost" onClick={onDone}>
        I have copied it
      </Button>
    </section>
  );
}
