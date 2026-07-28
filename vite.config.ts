import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react-swc";
import { defineConfig } from "vite";

import { resolve } from 'path'

const projectRoot = process.env.PROJECT_ROOT || import.meta.dirname

// https://vite.dev/config/
export default defineConfig({
  server: {
    // In dev, run `kubectl proxy --port=8001` and Vite forwards /api and /apis to it.
    proxy: {
      '/api': { target: 'http://localhost:8001', changeOrigin: true },
      '/apis': { target: 'http://localhost:8001', changeOrigin: true },
    },
  },
  plugins: [
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      '@': resolve(projectRoot, 'src')
    }
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      include: ['src/lib/**/*.ts'],
      exclude: ['src/lib/types.ts']
    }
  }
});
