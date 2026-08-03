import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Built output is committed into the Go server's embed tree so the binary is
// self-contained (internal/server/adminui).
export default defineConfig({
  plugins: [react()],
  base: '/admin/',
  build: {
    outDir: '../../internal/server/adminui/dist',
    emptyOutDir: true,
  },
  server: {
    // Dev against a locally running teslcam-server (scripts/run-server-local.sh).
    proxy: {
      '/admin/api': 'http://127.0.0.1:8090',
      '/events': 'http://127.0.0.1:8090',
      '/usage': 'http://127.0.0.1:8090',
    },
  },
})
