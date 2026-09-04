import { webcrypto } from "node:crypto";
import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// React Testing Library does not unmount between tests on its own, so a
// component left mounted keeps its timers, its listeners and its fetch in
// flight — which surfaces later as a different test failing for no reason.
afterEach(cleanup);

// jsdom ships getRandomValues but not SubtleCrypto, and PKCE needs the digest.
// Node's own implementation is the same algorithm the browser would use, so
// the tests exercise the real code path rather than a stub of it.
if (!globalThis.crypto?.subtle) {
  Object.defineProperty(globalThis, "crypto", { value: webcrypto, configurable: true });
}
