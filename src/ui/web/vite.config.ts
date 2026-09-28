/// <reference types="vitest/config" />
import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// Dev loop: run the Go devserver (`go run ./tools/devserver -port 8765`), then
//   VITE_API=http://127.0.0.1:8765 VITE_TOKEN=<token> npm run dev
// The proxy injects the bearer token and rewrites Origin so the core's
// DNS-rebinding/Origin checks accept the proxied requests (HTTP and WebSocket).
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_');
  const target = env.VITE_API;
  const token = env.VITE_TOKEN;
  return {
    base: './',
    plugins: [react()],
    build: {
      outDir: 'dist',
      emptyOutDir: true,
      assetsDir: 'assets',
      sourcemap: false,
      target: 'es2022',
    },
    server: target
      ? {
          proxy: {
            '/api': {
              target,
              changeOrigin: true,
              ws: true,
              configure(proxy) {
                proxy.on('proxyReq', (req) => {
                  req.setHeader('origin', target);
                  if (token) req.setHeader('authorization', `Bearer ${token}`);
                });
                proxy.on('proxyReqWs', (req) => {
                  req.setHeader('origin', target);
                  if (token) req.setHeader('authorization', `Bearer ${token}`);
                });
              },
            },
          },
        }
      : undefined,
    test: {
      environment: 'jsdom',
      include: ['src/**/*.test.{ts,tsx}'],
      setupFiles: ['src/test/setup.ts'],
      css: false,
    },
  };
});
