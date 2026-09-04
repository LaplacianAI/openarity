import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";
import { routeTree } from "./routeTree.gen";

// No basepath. The route files are named ui.*, so every route already carries
// the /ui prefix the Go server mounts the app under — setting basepath as well
// applies it twice and lands the browser on /ui/ui. One or the other, and the
// file names are the half that is visible in the tree.
const router = createRouter({
  routeTree,
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
