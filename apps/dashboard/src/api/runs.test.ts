import { afterEach, expect, it, vi } from "vitest";

import { brain, chunks, TEAM } from "@/test/brain";
import { FOLLOW_ATTEMPTS, followRunEvents, RESUME_AFTER_MS } from "./index";
import type { RunEvent } from "./types";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("hands over data that is not JSON as the text it was", async () => {
  brain({
    "GET /teams/{id}/runs/{rid}/events": {
      stream: chunks(
        "id: 1\nevent: note\ndata: not {json\n\n",
        'event: end\ndata: {"status":"failed"}\n\n',
      ),
    },
  });

  const got: RunEvent[] = [];
  await followRunEvents(TEAM, "r1", (e) => got.push(e), new AbortController().signal);

  expect(got).toEqual([
    { seq: 1, kind: "note", data: "not {json" },
    { seq: null, kind: "end", data: { status: "failed" } },
  ]);
});

// Waiting to follow a cut stream again must not outlive the person who
// stopped looking: aborting ends the wait at once, and nothing is fetched.
it("stops waiting to follow again as soon as it is aborted", async () => {
  const seen = brain({
    "GET /teams/{id}/runs/{rid}/events": () => ({
      stream: chunks("id: 1\nevent: step\ndata: {}\n\n"),
    }),
  });
  const abort = new AbortController();
  const got: RunEvent[] = [];

  const following = followRunEvents(TEAM, "r1", (e) => got.push(e), abort.signal);
  await vi.waitFor(() => expect(got).toHaveLength(1));
  const started = performance.now();
  abort.abort();
  await following;

  expect(performance.now() - started).toBeLessThan(RESUME_AFTER_MS / 2);
  expect(seen).toHaveLength(1);
});

/** A stream that delivers these frames, then fails as a dropped connection does. */
function dropsAfter(text: string): ReadableStream<Uint8Array> {
  let sent = false;
  return new ReadableStream<Uint8Array>({
    // Erroring a stream drops what it still holds, so the frames go first and
    // the error comes on the next read.
    pull(c) {
      if (sent) {
        c.error(new TypeError("network error"));
        return;
      }
      sent = true;
      c.enqueue(new TextEncoder().encode(text));
    },
  });
}

// A dropped connection is not the run failing: it is followed again from the
// last event seen, as a stream that closed early is.
it("follows again after the connection drops, from the last event it saw", async () => {
  let calls = 0;
  const seen = brain({
    "GET /teams/{id}/runs/{rid}/events": () => {
      calls += 1;
      return calls === 1
        ? { stream: dropsAfter("id: 1\nevent: step\ndata: {}\n\n") }
        : {
            stream: chunks(
              'id: 2\nevent: finished\ndata: {}\n\nevent: end\ndata: {"status":"succeeded"}\n\n',
            ),
          };
    },
  });
  const got: RunEvent[] = [];

  await followRunEvents(TEAM, "r1", (e) => got.push(e), new AbortController().signal);

  expect(got.map((e) => e.kind)).toEqual(["step", "finished", "end"]);
  expect(seen.map((s) => s.path)).toEqual([
    `/teams/${TEAM}/runs/r1/events`,
    `/teams/${TEAM}/runs/r1/events?after=1`,
  ]);
});

// Trying again cannot change a refusal: it ends the follow at once.
it("stops at a refusal rather than trying again", async () => {
  const seen = brain({
    "GET /teams/{id}/runs/{rid}/events": { status: 403, text: "forbidden" },
  });

  await expect(
    followRunEvents(TEAM, "r1", () => {}, new AbortController().signal),
  ).rejects.toMatchObject({ status: 403 });
  expect(seen).toHaveLength(1);
});

// A brain that stays out of reach is given up on, after waits that grow, so
// the trace says so rather than trying forever.
it("gives up after repeated drops with nothing read, waiting longer each time", async () => {
  vi.useFakeTimers();
  try {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => {
        throw new TypeError("network error");
      }),
    );
    const following = followRunEvents(TEAM, "r1", () => {}, new AbortController().signal);
    const outcome = following.then(
      () => "resolved",
      (err: Error) => err.message,
    );

    await vi.advanceTimersByTimeAsync(RESUME_AFTER_MS);
    expect(fetch).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(RESUME_AFTER_MS);
    // The second wait is twice the first.
    expect(fetch).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(60_000);

    expect(await outcome).toBe("network error");
    expect(fetch).toHaveBeenCalledTimes(FOLLOW_ATTEMPTS);
  } finally {
    vi.useRealTimers();
  }
});

// Only drops with nothing read between them count against it: a long run on
// a poor connection that keeps making progress is followed to its end.
it("keeps following a stream that drops often but keeps moving", async () => {
  vi.useFakeTimers();
  try {
    const drops = FOLLOW_ATTEMPTS + 2;
    let calls = 0;
    brain({
      "GET /teams/{id}/runs/{rid}/events": () => {
        calls += 1;
        return calls <= drops
          ? { stream: dropsAfter(`id: ${calls}\nevent: step\ndata: {}\n\n`) }
          : { stream: chunks('event: end\ndata: {"status":"succeeded"}\n\n') };
      },
    });
    const got: RunEvent[] = [];
    const following = followRunEvents(TEAM, "r1", (e) => got.push(e), new AbortController().signal);

    for (let i = 0; i <= drops; i++) await vi.advanceTimersByTimeAsync(RESUME_AFTER_MS);
    await following;

    expect(got.filter((e) => e.kind === "step")).toHaveLength(drops);
    expect(got.at(-1)?.kind).toBe("end");
  } finally {
    vi.useRealTimers();
  }
});
