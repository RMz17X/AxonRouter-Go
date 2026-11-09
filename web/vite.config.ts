import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const appVersion = (() => {
  try {
    return fs.readFileSync(path.resolve(__dirname, '../VERSION'), 'utf-8').trim()
  } catch {
    return ''
  }
})()

export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(appVersion),
  },
  plugins: [svelte(), tailwindcss()],
  server: {
    proxy: {
      '/api': 'http://localhost:3177',
      '/v1': 'http://localhost:3177',
      '/usage': 'http://localhost:3177',
      '/translator': 'http://localhost:3177',
      '/debug': 'http://localhost:3177',
      '/admin': 'http://localhost:3177',
    },
  },
})
