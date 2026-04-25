import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// Machine-specific dev-server config (tunnel hostnames, alternate backend
// ports) lives in `frontend/.env.local` and is git-ignored, so this file
// never has to be edited per-machine. See `frontend/.env.local.example`
// for the supported keys.
export default defineConfig(({ mode }) => {
  // loadEnv reads .env, .env.local, .env.[mode], .env.[mode].local from the
  // frontend/ directory. The empty prefix means we get raw keys (not just
  // VITE_*-prefixed ones) since these knobs are for vite itself, not the
  // browser bundle.
  const env = loadEnv(mode, process.cwd(), '')

  // Hosts vite is allowed to serve on. Defaults are safe for local dev.
  // For tunnel exposure, set A3C_ALLOWED_HOSTS to a comma-separated list of
  // public hostnames in frontend/.env.local — vite refuses unknown
  // hostnames by default and that's how it should stay in source control.
  const allowedHosts = env.A3C_ALLOWED_HOSTS
    ? env.A3C_ALLOWED_HOSTS.split(',').map((s) => s.trim()).filter(Boolean)
    : ['localhost', '127.0.0.1']

  // Where this dev server proxies /api/* to. Override A3C_BACKEND_URL if
  // your backend runs on a non-default port or a different host.
  const backendUrl = env.A3C_BACKEND_URL || 'http://127.0.0.1:3003'

  return {
    plugins: [react()],
    resolve: {
      alias: {
        '@': path.resolve(__dirname, './src'),
      },
    },
    server: {
      port: 3303,
      host: true,
      allowedHosts,
      proxy: {
        '/api': {
          target: backendUrl,
          changeOrigin: true,
        },
      },
    },
  }
})
