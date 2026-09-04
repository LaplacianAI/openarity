import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";
import { routeTree } from "./routeTree.gen";

// basepath, not a server rewrite: the app is served under /ui by the Go
// binary, so every link and every history entry has to carry that prefix or a
// reload lands on a path the router has never heard of.
const router = createRouter({
  routeTree,
  basepath: "/ui",
  defaultPreload: "intent",
  scrollRestoration: true,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("index.html has no #root — the build is wrong, not the runtime");
}

createRoot(root).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
