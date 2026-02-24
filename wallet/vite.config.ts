import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'

export default defineConfig({
    plugins: [vue(), tailwindcss()],
    base: './',   // Chrome 扩展必须用相对路径
    build: {
        outDir: resolve(__dirname, 'dist/popup'),
        emptyOutDir: true,
    },
    server: {
        port: 5174,
    },
})
