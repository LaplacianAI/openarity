import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  // Fixed, because tauri.conf.json names it: the window loads this address in
  // development and a chosen-at-random port would leave it blank.
  server: { port: 5174, strictPort: true },
  build: { outDir: "dist", emptyOutDir: true },
});
