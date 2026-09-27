// 通用格式化：相对时间 / 绝对时间 / 数字缩写 / 字节数。
import i18n from '../i18n'

const t = (k: string, v?: Record<string, unknown>) => i18n.global.t(k, v ?? {})

// 相对时间：如 5分钟前 / 1天前 / 3个月前
export function timeAgo(ts: string): string {
  const date = toDate(ts)
  const diff = date ? Date.now() - date.getTime() : NaN
  if (isNaN(diff)) return '-'
  const min = Math.floor(diff / 60000)
  if (min < 1) return t('common.justNow')
  if (min < 60) return t('common.minutesAgo', { n: min })
  const h = Math.floor(min / 60)
  if (h < 24) return t('common.hoursAgo', { n: h })
  const d = Math.floor(h / 24)
  if (d < 30) return t('common.daysAgo', { n: d })
  const mo = Math.floor(d / 30)
  if (mo < 12) return t('common.monthsAgo', { n: mo })
  return t('common.yearsAgo', { n: Math.floor(mo / 12) })
}

// 时间戳统一按设备本地时区渲染（与核心时区修复对齐）：
// 核心经 gorm/mattn-sqlite3 往返后输出 RFC3339（带 Z 或 ±hh:mm 偏移），也可能有
// 无偏移的本地形态——new Date() 对三种形态都能得到正确时刻（无偏移按本地解析），
// 再用本地时区格式化，任务执行历史等不再出现"比本机晚 8 小时"的 UTC 直显。
// 解析失败（空/异常串）原样返回，不吞信息。
function toDate(ts: string): Date | null {
  // 纯日期（YYYY-MM-DD）按本地零点解析（new Date 对 ISO 日期形态按 UTC，需绕开）
  if (/^\d{4}-\d{2}-\d{2}$/.test(ts)) {
    const [y, m, d] = ts.split('-').map(Number)
    return new Date(y, m - 1, d)
  }
  const parsed = new Date(ts)
  return isNaN(parsed.getTime()) ? null : parsed
}

const pad2 = (n: number) => String(n).padStart(2, '0')

// RFC3339 时间戳 → 本地时区 "YYYY-MM-DD HH:mm:ss"（空值显示 -）
export function fmtDateTime(ts?: string | null): string {
  if (!ts) return '-'
  const date = toDate(ts)
  if (!date) return ts
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())} ${pad2(date.getHours())}:${pad2(date.getMinutes())}:${pad2(date.getSeconds())}`
}

// fmtTime：fmtDateTime 的旧名（账号详情/最后刷新等沿用）
export const fmtTime = fmtDateTime

// 数字缩写：1.2M / 3.4K
export function fmtCompact(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return String(n)
}

// 千分位；未采集显示 -
export function fmtNum(v: number | string | undefined | null): string {
  if (v === undefined || v === null || v === '') return '-'
  const n = Number(v)
  return Number.isFinite(n) ? n.toLocaleString('zh-CN', { maximumFractionDigits: 2 }) : String(v)
}

// 字节数：1.2 GB / 3.4 MB
export function fmtBytes(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(2) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB'
  if (n >= 1024) return (n / 1024).toFixed(1) + ' KB'
  return n + ' B'
}
