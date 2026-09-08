import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react-swc";
import { defineConfig } from "vite";

import { resolve } from 'path'

const projectRoot = process.env.PROJECT_ROOT || import.meta.dirname

// https://vite.dev/config/
export default defineConfig({
  server: {
    // In dev, run `kubectl proxy --port=8001` and Vite forwards /api and /apis to it.
    //
    // /auth is a separate target because `kubectl proxy` serves no /auth at
    // all: the console's gate calls `currentSession()` before it renders
    // anything, so without this the dev server lands on a login screen that
    // cannot work. Point AUTH_PROXY_TARGET at whatever is serving authd —
    // `kubectl -n cluster-control port-forward svc/cluster-control-auth
    // 8443:443` is the usual one, hence the https default and
    // `secure: false` (the in-cluster certificate names the Service, not
    // localhost). See docs/development.md.
    proxy: {
      '/api': { target: 'http://localhost:8001', changeOrigin: true },
      '/apis': { target: 'http://localhost:8001', changeOrigin: true },
      '/auth': {
        target: process.env.AUTH_PROXY_TARGET || 'https://localhost:8443',
        changeOrigin: true,
        secure: false,
      },
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
