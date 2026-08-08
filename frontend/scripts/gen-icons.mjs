#!/usr/bin/env node
/**
 * Lumo PWA 图标生成脚本（纯 Node 标准库，禁止 npm 依赖）。
 *
 * 用途：为 PWA manifest 生成暖渐变品牌图标（#FF5928 → #FBA029 对角渐变 + 白色
 * "L" 字形），输出 192/512 常规（purpose: any）与 512 maskable 三个 PNG 到
 * frontend/public/icons/，由 vite 构建时原样拷贝到 dist。
 *
 * 实现：不依赖 sharp/canvas——手写 PNG 二进制（8 字节签名 + IHDR/IDAT/IEND chunk，
 * CRC32 查表自实现，IDAT 用 node:zlib deflateSync 压缩 RGBA 扫描线）。
 * 幂等：每次运行直接覆写同名文件，可重复执行。
 *
 * 品牌色仅此脚本 + manifest.json（theme_color/background_color）出现——
 * 属 PWA 规范要求的二进制/元数据字面量，不违反"颜色字面量仅 tokens.css"约束。
 */
import { deflateSync } from 'node:zlib'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = dirname(dirname(fileURLToPath(import.meta.url))) // frontend/
const OUT_DIR = join(ROOT, 'public', 'icons')

/** 品牌暖焰渐变（与 tokens.css --color-primary 同源） */
const C1 = [255, 89, 40] // #FF5928
const C2 = [251, 160, 41] // #FBA029
const WHITE = [255, 255, 255]

/** "L" 字形在归一化坐标 [0,1] 内的两块矩形（竖条 + 底横条），均落在 maskable 安全区 [0.2,0.8] 内 */
const BARS = [
  [0.32, 0.3, 0.42, 0.7], // 竖条：x0,y0,x1,y1
  [0.32, 0.6, 0.66, 0.7], // 底横条
]

// ---------- CRC32（PNG chunk 校验） ----------
const CRC_TABLE = (() => {
  const t = new Uint32Array(256)
  for (let n = 0; n < 256; n++) {
    let c = n
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
    t[n] = c >>> 0
  }
  return t
})()

function crc32(buf) {
  let c = 0xffffffff
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8)
  return (c ^ 0xffffffff) >>> 0
}

function chunk(type, data) {
  const len = Buffer.alloc(4)
  len.writeUInt32BE(data.length)
  const typeBuf = Buffer.from(type, 'ascii')
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(Buffer.concat([typeBuf, data])))
  return Buffer.concat([len, typeBuf, data, crc])
}

// ---------- 像素着色 ----------
function lerp(a, b, t) {
  return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t]
}

/** 矩形带 1.5px 抗锯齿的覆盖率（0..1）；hypot 给出轻微圆角，观感更精致 */
function rectCoverage(px, py, size, bar) {
  const [nx0, ny0, nx1, ny1] = bar
  const x0 = nx0 * size
  const x1 = nx1 * size
  const y0 = ny0 * size
  const y1 = ny1 * size
  const dx = Math.max(x0 - px, px - x1, 0)
  const dy = Math.max(y0 - py, py - y1, 0)
  const d = Math.hypot(dx, dy)
  const aa = 1.5
  return Math.min(1, Math.max(0, 1 - d / aa))
}

/** "L" 字形整体覆盖率（两矩形取并集，取 max 保证重叠区不叠加） */
function glyphCoverage(px, py, size) {
  let cov = 0
  for (const bar of BARS) cov = Math.max(cov, rectCoverage(px, py, size, bar))
  return cov
}

function pixel(px, py, size) {
  const nx = px / (size - 1)
  const ny = py / (size - 1)
  // 对角渐变：左上 #FF5928 → 右下 #FBA029
  const t = Math.min(1, Math.max(0, (nx + ny) / 2))
  let rgb = lerp(C1, C2, t)
  // 轻微暗角：四角最多压暗 12%，让中心 L 更突出
  const dx = nx - 0.5
  const dy = ny - 0.5
  const dist = Math.min(1, Math.hypot(dx, dy) * 1.6)
  const vignette = 1 - 0.12 * dist
  rgb = [rgb[0] * vignette, rgb[1] * vignette, rgb[2] * vignette]
  // 叠加白色 L（抗锯齿覆盖率混合）
  const cov = glyphCoverage(px, py, size)
  const out = lerp(rgb, WHITE, cov)
  return [Math.round(out[0]), Math.round(out[1]), Math.round(out[2]), 255]
}

// ---------- PNG 编码 ----------
function encodePng(size) {
  const stride = size * 4
  const raw = Buffer.alloc((stride + 1) * size) // 每行 1 字节 filter=0 前缀
  for (let y = 0; y < size; y++) {
    const rowStart = y * (stride + 1)
    raw[rowStart] = 0
    for (let x = 0; x < size; x++) {
      const [r, g, b, a] = pixel(x, y, size)
      const o = rowStart + 1 + x * 4
      raw[o] = r
      raw[o + 1] = g
      raw[o + 2] = b
      raw[o + 3] = a
    }
  }
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(size, 0)
  ihdr.writeUInt32BE(size, 4)
  ihdr[8] = 8 // bit depth
  ihdr[9] = 6 // color type RGBA
  ihdr[10] = 0
  ihdr[11] = 0
  ihdr[12] = 0
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ])
}

mkdirSync(OUT_DIR, { recursive: true })
const targets = [
  ['icon-192.png', 192],
  ['icon-512.png', 512],
  ['icon-maskable-512.png', 512], // 与 any 同图：全出血暖渐变背景 + L 在安全区内，天然满足 maskable
]
for (const [name, size] of targets) {
  const file = join(OUT_DIR, name)
  const buf = encodePng(size)
  writeFileSync(file, buf)
  console.log(`generated ${file} (${size}x${size}, ${(buf.length / 1024).toFixed(1)} KiB)`)
}
console.log('icons written to', OUT_DIR)
