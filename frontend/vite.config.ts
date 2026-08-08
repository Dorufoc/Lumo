import { fileURLToPath, URL } from 'node:url'
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'

/**
 * PWA 内联插件（无外部依赖）：
 * closeBundle 阶段收集 dist 内的全部构建产物（应用壳 + hashed assets + public 拷贝的
 * manifest/icons），替换 sw.template.js 中的 __PRECACHE_MANIFEST__ 占位符，
 * 生成带当次构建哈希清单的 dist/sw.js——保证每次构建预缓存列表与产物一致。
 *
 * 顺序说明：vite 在 prepareOutDir（构建早期）就把 public/ 拷贝到 dist/，
 * closeBundle（所有产物写盘后）再扫描 dist 一定包含全部文件，无需 emitFile 时序假设。
 */
function lumoPwaPlugin(): Plugin {
  const swTemplate = fileURLToPath(new URL('./scripts/sw.template.js', import.meta.url))
  // 精确匹配模板中的代码行（模板注释里也出现占位符字样，故不能只 replace 裸占位符）
  const placeholderLine = 'const PRECACHE_MANIFEST = __PRECACHE_MANIFEST__'
  return {
    name: 'lumo-pwa',
    apply: 'build',
    closeBundle() {
      const outDir = fileURLToPath(new URL('./dist', import.meta.url))
      if (!existsSync(outDir)) return

      // 递归收集 dist 相对路径（如 /index.html、/assets/x-abc.js、/icons/icon-192.png）
      const collect = (dir: string): string[] => {
        const out: string[] = []
        for (const entry of readdirSync(dir, { withFileTypes: true })) {
          const abs = join(dir, entry.name)
          if (entry.isDirectory()) out.push(...collect(abs))
          // abs.slice 已含前导分隔符（如 \assets\x.js → /assets/x.js），无需再拼 '/'
          else out.push(abs.slice(outDir.length).replaceAll('\\', '/'))
        }
        return out
      }

      // 预缓存 = 根路径（导航 fallback 键）+ 全部产物（sw.js 自身除外）
      const files = collect(outDir).filter((f) => f !== '/sw.js')
      const precache = ['/', ...files]

      const sw = readFileSync(swTemplate, 'utf8').replace(
        placeholderLine,
        `const PRECACHE_MANIFEST = ${JSON.stringify(precache, null, 2)}`,
      )
      writeFileSync(join(outDir, 'sw.js'), sw)
    },
  }
}

export default defineConfig({
  plugins: [vue(), lumoPwaPlugin()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8787',
      '/health': 'http://127.0.0.1:8787',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
