import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// base is "/ui/" and not "/": the SPA is mounted under a prefix so it cannot
// swallow /teams, /users or /auth, and asset URLs have to agree with that or
// the page loads and every script 404s.
//
// The build stays in this directory. Getting it into the brain binary is the
// Makefile's job, not this file's — a Vite config that knows where a Go
// package lives breaks the day somebody moves one.
export default defineConfig({
  plugins: [react()],
  base: "/ui/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    // `npm run dev` serves the SPA on its own port and proxies the API to a
    // brain running on the host, so the dev loop keeps hot reload without the
    // page being cross-origin from what it calls.
    proxy: {
      "/auth": "http://127.0.0.1:21120",
      "/teams": "http://127.0.0.1:21120",
      "/users": "http://127.0.0.1:21120",
      "/whoami": "http://127.0.0.1:21120",
    },
  },
});
