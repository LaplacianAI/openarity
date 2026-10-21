/**
 * Server-sent events read off a fetch body, because EventSource cannot carry
 * the token. Only the parts of the format the brain uses: `id`, `event`,
 * `data` and comments. `retry` is ignored.
 */

export type SSEFrame = {
  id: string | null;
  event: string;
  data: string;
};

/**
 * Yields each frame as it completes. A read can end anywhere — mid-line,
 * between a CR and its LF, inside a multi-byte character — so nothing is
 * parsed until the blank line that ends a frame has arrived.
 */
export async function* readFrames(
  body: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
): AsyncGenerator<SSEFrame> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (!signal?.aborted) {
      const { done, value } = await reader.read();
      buffer += done ? decoder.decode() : decoder.decode(value, { stream: true });
      // A CR at the very end may be half of a CRLF still in flight.
      const held = !done && buffer.endsWith("\r") ? "\r" : "";
      if (held) buffer = buffer.slice(0, -1);
      buffer = buffer.replace(/\r\n?/g, "\n");

      let end = buffer.indexOf("\n\n");
      while (end !== -1) {
        const frame = parseFrame(buffer.slice(0, end));
        buffer = buffer.slice(end + 2);
        if (frame) yield frame;
        end = buffer.indexOf("\n\n");
      }
      buffer += held;
      if (done) return;
    }
  } finally {
    reader.cancel().catch(() => {});
  }
}

function parseFrame(text: string): SSEFrame | null {
  let id: string | null = null;
  let event = "message";
  const data: string[] = [];
  // A comment line, `: keep-alive`, has an empty field name and so matches
  // nothing below.
  for (const line of text.split("\n")) {
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "data") data.push(value);
    else if (field === "event") event = value;
    else if (field === "id") id = value;
  }
  // A frame of comments only, such as a keep-alive, dispatches nothing.
  return data.length === 0 ? null : { id, event, data: data.join("\n") };
}
