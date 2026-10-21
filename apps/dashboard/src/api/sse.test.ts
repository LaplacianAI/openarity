import { describe, expect, it } from "vitest";

import { chunks } from "@/test/brain";
import { readFrames, type SSEFrame } from "./sse";

async function read(...parts: (string | Uint8Array)[]): Promise<SSEFrame[]> {
  const got: SSEFrame[] = [];
  for await (const f of readFrames(chunks(...parts))) got.push(f);
  return got;
}

const WIRE =
  ": following run r1\n\n" +
  'id: 1\nevent: started\ndata: {"agent":"helper"}\n\n' +
  ": keep-alive\n\n" +
  'id: 2\nevent: tool_call\ndata: {"name":"search"}\n\n' +
  'event: end\ndata: {"status":"succeeded"}\n\n';

const FRAMES: SSEFrame[] = [
  { id: "1", event: "started", data: '{"agent":"helper"}' },
  { id: "2", event: "tool_call", data: '{"name":"search"}' },
  { id: null, event: "end", data: '{"status":"succeeded"}' },
];

describe("readFrames", () => {
  it("reads frames and skips comments", async () => {
    expect(await read(WIRE)).toEqual(FRAMES);
  });

  // A read can end anywhere, so every place the wire could be cut must give
  // the same frames as no cut at all.
  it("gives the same frames wherever the stream is split", async () => {
    for (const wire of [WIRE, WIRE.replaceAll("\n", "\r\n"), WIRE.replaceAll("\n", "\r")]) {
      for (let at = 1; at < wire.length; at++) {
        expect(await read(wire.slice(0, at), wire.slice(at))).toEqual(FRAMES);
      }
    }
  });

  it("keeps a character whose bytes arrive in two reads", async () => {
    const bytes = new TextEncoder().encode('event: x\ndata: "é"\n\n');
    const mid = bytes.indexOf(0xc3) + 1;
    expect(await read(bytes.slice(0, mid), bytes.slice(mid))).toEqual([
      { id: null, event: "x", data: '"é"' },
    ]);
  });

  it("joins data lines and reads a field with no space or no value", async () => {
    expect(await read("data:one\ndata: two\nid\nevent\n\n")).toEqual([
      { id: "", event: "", data: "one\ntwo" },
    ]);
  });

  it("names a frame with no event line a message", async () => {
    expect(await read("data: hi\n\n")).toEqual([{ id: null, event: "message", data: "hi" }]);
  });

  // The format says a frame is dispatched by its blank line; a stream cut
  // before one has not finished saying it.
  it("drops a frame the stream ended in the middle of", async () => {
    expect(await read('id: 1\nevent: started\ndata: {"agent":"x"}\n')).toEqual([]);
  });

  it("stops reading once the signal has aborted", async () => {
    const abort = new AbortController();
    abort.abort();
    const got: SSEFrame[] = [];
    for await (const f of readFrames(chunks(WIRE), abort.signal)) got.push(f);
    expect(got).toEqual([]);
  });
});
