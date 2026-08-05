// 中文（简体）语言包。
// 本文件是 i18n 的「类型权威」：`Messages` 类型由 zhCN 的实际结构推导而来，
// en-US.ts 以 `Messages` 类型实现 → en-US 缺 key / 多 key 都会在编译期报错（机械性结构对齐）。

const zhCN = {
  app: {
    title: 'Lumo AI',
    tagline: '本地优先的智能刷题与学习平台',
  },
  nav: {
    dashboard: '仪表盘',
    library: '题库',
    settings: '设置',
  },
  common: {
    save: '保存',
    cancel: '取消',
    confirm: '确认',
    delete: '删除',
    back: '返回',
    retry: '重试',
    language: '语言',
    greeting: '你好，{name}！',
  },
}

// 把 zhCN 的字面量值类型放宽为 string，同时完整保留 key 嵌套结构 →
// 供 en-US 按完全相同结构校验（缺/多 key 即编译错误）。
type DeepString<T> = { [K in keyof T]: T[K] extends string ? string : DeepString<T[K]> }

/** 语言包结构类型（由 zh-CN 推导，en-US 必须满足同一结构）。 */
export type Messages = DeepString<typeof zhCN>

export { zhCN }
