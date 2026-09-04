import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

// React Testing Library does not unmount between tests on its own, so a
// component left mounted keeps its timers, its listeners and its fetch in
// flight — which surfaces later as a different test failing for no reason.
afterEach(cleanup);
