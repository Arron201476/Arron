import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  build: {
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            { name: "react-vendor", test: /node_modules[\\/]react(-dom)?[\\/]/, priority: 20 },
            { name: "lexical-vendor", test: /node_modules[\\/](@lexical|lexical)[\\/]/, priority: 20 },
          ],
        },
      },
    },
  },
  server: {
    fs: {
      allow: [".."],
    },
    host: "127.0.0.1",
    port: 8832,
  },
  preview: {
    host: "127.0.0.1",
    port: 8832,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    css: true,
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      reportsDirectory: "coverage",
      exclude: ["src/test/**", "src/**/*.test.*", "src/main.tsx"],
    },
  },
});
