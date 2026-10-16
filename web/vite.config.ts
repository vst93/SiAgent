import { defineConfig } from "vite";
export default defineConfig({
  server: {
    proxy: {
      "/health": "http://127.0.0.1:8080",
      "/api": "http://127.0.0.1:8080",
      "/callbacks": "http://127.0.0.1:8080",
    },
  },
});
