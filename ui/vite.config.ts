import { defineConfig, loadEnv } from 'vite'

import { tanstackRouter } from '@tanstack/router-plugin/vite'

import viteReact from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const config = defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const target =
    process.env.API_PROXY_TARGET ??
    env.API_PROXY_TARGET ??
    'http://127.0.0.1:8080'
  return {
    server: {
      host: '127.0.0.1',
      port: 3000,
      strictPort: true,
      proxy: {
        '/v1': { target, changeOrigin: true },
        '/docs': { target, changeOrigin: true },
        '/openapi.json': { target, changeOrigin: true },
      },
    },
    resolve: { tsconfigPaths: true },
    plugins: [
      tailwindcss(),
      tanstackRouter({ target: 'react', autoCodeSplitting: true }),
      viteReact(),
    ],
  }
})

export default config
