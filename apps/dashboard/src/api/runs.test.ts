import { afterEach, expect, it, vi } from "vitest";

import { brain, chunks, TEAM } from "@/test/brain";
import { followRunEvents, RESUME_AFTER_MS } from "./index";
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
