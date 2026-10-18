import { defineConfig } from "vite";
export default defineConfig({
  // Relative assets let the same bundle work at /, /siagent/, or another prefix.
  base: "./",
  server: {
    proxy: {
      "/health": "http://127.0.0.1:8080",
      "/api": "http://127.0.0.1:8080",
      "/callbacks": "http://127.0.0.1:8080",
    },
  },
});
