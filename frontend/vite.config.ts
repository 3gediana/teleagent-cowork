import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 3303,
    host: true,
    allowedHosts: [
      't1dwvu6bzdng.vip3.xiaomiqiu123.top',
      '1y2ae99xyr7b.vip3.xiaomiqiu123.top',
      'fjgjfh9on872.vip3.xiaomiqiu123.top',
    ],
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:3003',
        changeOrigin: true,
      },
    },
  },
})