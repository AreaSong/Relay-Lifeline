import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "/admin/",
  server: {
    proxy: {
      "/admin/api": {
        target: "http://127.0.0.1:8318",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 800,
  },
});
