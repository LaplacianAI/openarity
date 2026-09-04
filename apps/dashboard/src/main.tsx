import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";

const root = document.getElementById("root");
if (!root) {
  throw new Error("index.html has no #root — the build is wrong, not the runtime");
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
