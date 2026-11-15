import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, TriangleAlert } from "lucide-react";
import QRCode from "qrcode";
import { useEffect, useState } from "react";

import { getChannelPairing, pairChannel } from "@/api";
import type { ChannelPairing, PairChannelRequest } from "@/api/types";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

/** How often a pairing under way is read: a QR changes about every 20 seconds. */
export const PAIR_POLL_MS = 2000;

/**
 * Linking a WhatsApp number to a channel: by a QR code to scan, or a code
 * for a number. Only for someone with channel:write, since the QR is what
 * lets a phone link the channel. The pairing is read every two seconds
 * while it is under way and dropped from the cache when the sheet closes, so
 * a QR is never left behind.
 */
export function LinkNumber({ teamId, channelId }: { teamId: string; channelId: string }) {
  const qc = useQueryClient();
  const key = ["pairing", teamId, channelId];
  const pairing = useQuery({
    queryKey: key,
    queryFn: () => getChannelPairing(teamId, channelId),
    refetchInterval: (q) => (q.state.data?.state === "pairing" ? PAIR_POLL_MS : false),
    gcTime: 0,
  });
  const ask = useMutation({
    mutationFn: (body: PairChannelRequest) => pairChannel(teamId, channelId, body),
    onSuccess: (p) => qc.setQueryData(key, p),
  });

  if (pairing.isPending) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        Reading the number…
      </p>
    );
  }
  if (pairing.isError) {
    return (
      <ErrorState
        what="the number"
        error={pairing.error}
        onRetry={() => pairing.refetch()}
        retrying={pairing.isRefetching}
      />
    );
  }

  const p = pairing.data;
  return (
    <div className="space-y-2">
      {!p.held ? (
        <p role="alert" className="flex gap-2 text-sm">
          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
          <span>This deployment runs no WhatsApp role, so nothing pairs and nothing is sent.</span>
        </p>
      ) : null}
      {p.state === "connected" ? (
        <Linked phone={p.phone} onRelink={(body) => ask.mutate(body)} asking={ask.isPending} />
      ) : p.state === "pairing" ? (
        <Pairing p={p} />
      ) : (
        <NotLinked error={p.error} onAsk={(body) => ask.mutate(body)} asking={ask.isPending} />
      )}
      {ask.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {ask.error.message}
        </p>
      ) : null}
    </div>
  );
}

/** A number as people read one: the country code after a +. */
export function shownNumber(phone?: string): string {
  return phone ? `+${phone}` : "a number";
}

function Linked({
  phone,
  onRelink,
  asking,
}: {
  phone?: string;
  onRelink: (body: PairChannelRequest) => void;
  asking: boolean;
}) {
  const [confirming, setConfirming] = useState(false);
  return (
    <>
      <p className="text-sm">
        <span className="font-medium tabular-nums">{shownNumber(phone)}</span>, linked.
      </p>
      {confirming ? (
        <div className="space-y-2 border-l-2 border-rule pl-3">
          <p className="text-sm text-muted-foreground">
            Linking another number logs {shownNumber(phone)} out of this channel.
          </p>
          <div className="flex gap-2">
            <Button size="sm" loading={asking} onClick={() => onRelink({ method: "qr" })}>
              Link another number
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setConfirming(false)}>
              Keep this one
            </Button>
          </div>
        </div>
      ) : (
        <Button variant="outline" size="sm" onClick={() => setConfirming(true)}>
          Link another number
        </Button>
      )}
    </>
  );
}

function NotLinked({
  error,
  onAsk,
  asking,
}: {
  error?: string;
  onAsk: (body: PairChannelRequest) => void;
  asking: boolean;
}) {
  const [byCode, setByCode] = useState(false);
  const [phone, setPhone] = useState("");
  return (
    <>
      <p className="text-sm text-muted-foreground">
        Not linked. Use a number of the bot's own: a linked device is unofficial, and a number can
        be banned.
      </p>
      {error ? <p className="text-sm text-destructive">{error}</p> : null}
      {byCode ? (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (phone.trim() !== "") onAsk({ method: "code", phone: phone.trim() });
          }}
        >
          <label className="grid gap-1 text-xs font-medium" htmlFor="pair-phone">
            The number, with its country code
            <Input
              id="pair-phone"
              inputMode="tel"
              autoComplete="tel"
              placeholder="+91 98765 43210"
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              className="w-48"
            />
          </label>
          <Button type="submit" size="sm" loading={asking} disabled={phone.trim() === ""}>
            Get a code
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={() => setByCode(false)}>
            Use a QR code instead
          </Button>
        </form>
      ) : (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" loading={asking} onClick={() => onAsk({ method: "qr" })}>
            Show a QR code
          </Button>
          <Button size="sm" variant="outline" onClick={() => setByCode(true)}>
            Use a pairing code
          </Button>
        </div>
      )}
    </>
  );
}

function Pairing({ p }: { p: ChannelPairing }) {
  return (
    <>
      {p.code ? (
        <>
          <p className="text-sm text-muted-foreground">
            On the phone: WhatsApp, Settings, Linked devices, Link a device, then "Link with phone
            number instead", and enter
          </p>
          <p className="font-mono text-2xl tracking-widest">{p.code}</p>
        </>
      ) : p.qr ? (
        <>
          <p className="text-sm text-muted-foreground">
            Scan it from WhatsApp on the phone: Settings, Linked devices, Link a device. It changes
            about every 20 seconds.
          </p>
          <QR text={p.qr} />
        </>
      ) : null}
      <p role="status" className="flex items-center gap-1.5 text-sm text-muted-foreground">
        <LoaderCircle aria-hidden className="size-4 animate-spin motion-reduce:animate-none" />
        {p.qr || p.code ? "Waiting for the phone…" : "Waiting for WhatsApp…"}
      </p>
    </>
  );
}

/**
 * A QR code drawn as an image of its modules: the text it carries is never on
 * the page, and the image is dark on light whatever the theme, which is what
 * a phone's camera reads.
 */
function QR({ text }: { text: string }) {
  const [src, setSrc] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    QRCode.toString(text, { type: "svg", margin: 2, errorCorrectionLevel: "L" })
      .then((svg) => {
        if (live) setSrc(`data:image/svg+xml;utf8,${encodeURIComponent(svg)}`);
      })
      .catch(() => {
        if (live) setSrc(null);
      });
    return () => {
      live = false;
    };
  }, [text]);
  if (!src) return null;
  return <img src={src} alt="QR code to scan from WhatsApp" className="size-56 bg-white" />;
}
