import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, './src'),
    },
  },
  // The SPA is served by the Go backend under /admin/ (see
  // backend/cmd/server/main.go), never at the domain root.
  base: '/admin/',
  build: {
    // `npm run build`, run from webui/, writes straight into the Go module
    // that embeds it (backend/internal/webui/embed.go) — this only works
    // because webui/ and backend/ stay sibling directories, both locally
    // and in the Docker build stage (see backend/Dockerfile).
    outDir: path.resolve(import.meta.dirname, '../backend/internal/webui/dist'),
    emptyOutDir: true,
  },
})
