import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// 开发期把网关接口代理到本地 Go 进程（默认 8787）
const backend = process.env.AIGW_DEV_BACKEND ?? 'http://127.0.0.1:8787'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: 'dist',
    // 保留 dist 目录（含占位文件），否则 //go:embed all:dist 会因目录为空而编译失败
    emptyOutDir: false,
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': backend,
      '/admin/api': backend,
      '/health': backend,
    },
  },
})