import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const backendURL = process.env.CONTENT_AGENT_BACKEND_URL ?? "http://127.0.0.1:8850";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": { target: backendURL, changeOrigin: false },
      "/healthz": { target: backendURL, changeOrigin: false },
    },
  },
});
