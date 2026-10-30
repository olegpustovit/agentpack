import path from "node:path"
import { defineConfig } from "vitest/config"
import react, { reactCompilerPreset } from "@vitejs/plugin-react"
import babel from "@rolldown/plugin-babel"
import tailwindcss from "@tailwindcss/vite"
import { tanstackRouter } from "@tanstack/router-plugin/vite"

const goOrigin = "http://127.0.0.1:7420"

export default defineConfig({
  base: "/",
  plugins: [
    tanstackRouter({ target: "react", autoCodeSplitting: true }),
    react(),
    babel({ presets: [reactCompilerPreset()] }),
    tailwindcss(),
  ],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "src") } },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": { target: goOrigin, ws: true },
      "/mcp": { target: goOrigin },
      "/oauth": { target: goOrigin },
    },
  },
  build: { outDir: "dist", emptyOutDir: true },
  test: { environment: "jsdom", include: ["src/**/*.test.{ts,tsx}"] },
})
