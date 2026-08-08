/**
 * Lumo AI Service Worker 模板。
 *
 * 本文件不是最终产物：构建时由 vite.config.ts 内联插件读取本模板，
 * 将 __PRECACHE_MANIFEST__ 占位符替换为当次构建的产物清单（JSON 数组），
 * 写入 dist/sw.js。公共目录不放置 sw.js，避免与生成产物冲突。
 *
 * 缓存策略（版本化，仅保留当前版本）：
 *  - install   预缓存全部构建产物（应用壳 + hashed assets + manifest + icons），skipWaiting
 *  - activate  删除旧版本缓存（非 lumo-shell-*），clients.claim
 *  - navigate  network-first，失败回退缓存 /index.html（离线应用壳关键路径）
 *  - /api|/health GET  stale-while-revalidate（先回缓存、后台更新）
 *  - /assets/  cache-first（文件名含 hash 天然不可变）
 *  - 其余 GET  命中缓存则用，否则网络放行（不写入新缓存）
 *  - 非 GET / SSE（text/event-stream）一律放行，绝不进缓存（防断流）
 */
const CACHE_PREFIX = 'lumo-shell'
const CACHE_VERSION = 'v1'
const CACHE_NAME = `${CACHE_PREFIX}-${CACHE_VERSION}`

// 构建时由 vite 插件替换为产物清单，例如 ["/", "/index.html", "/assets/index-abc123.js", ...]
const PRECACHE_MANIFEST = __PRECACHE_MANIFEST__

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(PRECACHE_MANIFEST))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((k) => !k.startsWith(`${CACHE_PREFIX}-`)).map((k) => caches.delete(k))),
      )
      .then(() => self.clients.claim()),
  )
})

/** stale-while-revalidate：先回缓存，同时后台拉取更新缓存；网络失败回退缓存 */
async function staleWhileRevalidate(request) {
  const cache = await caches.open(CACHE_NAME)
  const cached = await cache.match(request)
  const network = fetch(request)
    .then((response) => {
      if (response.ok) cache.put(request, response.clone())
      return response
    })
    .catch(() => cached)
  return cached || network
}

/** cache-first：命中直接返回，未命中回源并写入缓存 */
async function cacheFirst(request) {
  const cache = await caches.open(CACHE_NAME)
  const cached = await cache.match(request)
  if (cached) return cached
  const response = await fetch(request)
  if (response.ok) cache.put(request, response.clone())
  return response
}

/** network-first：优先网络，失败回退离线应用壳 */
async function networkFirst(request) {
  try {
    const response = await fetch(request)
    if (response.ok) {
      const cache = await caches.open(CACHE_NAME)
      // 回源成功时把最新应用壳写回缓存（覆盖 /index.html 与 / 两条键）
      const url = new URL(request.url)
      const copy = response.clone()
      cache.put(request, copy)
      if (url.pathname === '/') cache.put('/index.html', response.clone())
    }
    return response
  } catch {
    const cache = await caches.open(CACHE_NAME)
    const shell = (await cache.match('/index.html')) || (await cache.match('/'))
    if (shell) return shell
    return Response.error()
  }
}

self.addEventListener('fetch', (event) => {
  const { request } = event
  const url = new URL(request.url)

  // 跨域与特殊方法放行
  if (url.origin !== self.location.origin) return
  if (request.method !== 'GET') return
  // SSE 流式响应绝不进缓存（防止断流）
  if ((request.headers.get('accept') || '').includes('text/event-stream')) return

  // 页面导航：network-first，失败回退应用壳
  if (request.mode === 'navigate') {
    event.respondWith(networkFirst(request))
    return
  }

  // API / 健康检查：stale-while-revalidate
  if (url.pathname.startsWith('/api/') || url.pathname === '/health') {
    event.respondWith(staleWhileRevalidate(request))
    return
  }

  // hashed 静态资源：cache-first
  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(cacheFirst(request))
    return
  }

  // 其余 GET（icons / manifest / 其他静态）：命中缓存用缓存，否则网络放行
  event.respondWith(
    caches.match(request).then((cached) => cached || fetch(request)),
  )
})
