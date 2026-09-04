import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// base is "/ui/" and not "/": the SPA is mounted under a prefix so it cannot
// swallow /teams, /users or /auth, and asset URLs have to agree with that or
// the page loads and every script 404s.
//
// The build stays in this directory. Getting it into the brain binary is the
// Makefile's job, not this file's — a Vite config that knows where a Go
// package lives breaks the day somebody moves one.
export default defineConfig({
  plugins: [
    // Before the React plugin, which is what the router plugin's own docs
    // require: it generates routeTree.gen.ts from src/routes and React has to
    // transform the result rather than the other way round.
    tanstackRouter({ target: "react", autoCodeSplitting: true }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: { "@": new URL("./src", import.meta.url).pathname },
  },
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
  test: {
    // jsdom rather than the default node environment: these are components,
    // and a component test without a DOM asserts nothing useful.
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
    coverage: {
      provider: "v8",
      reporter: ["text-summary"],
      include: ["src/**/*.{ts,tsx}"],
      // Entry points, generated route trees and vendored shadcn components.
      exclude: [
        "src/main.tsx",
        "src/test/**",
        "src/components/ui/**",
        "src/routeTree.gen.ts",
        "src/**/*.d.ts",
      ],
    },
  },
});
