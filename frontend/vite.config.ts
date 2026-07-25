import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import wails from '@wailsio/runtime/plugins/vite'

// Vite 同时启用 React、Tailwind CSS 4 与 Wails 绑定生成/开发桥接插件。
export default defineConfig({
  plugins: [react(), tailwindcss(), wails('./bindings')],
})
