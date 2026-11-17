/**
 * 北京时间（UTC+8）工具集。
 *
 * 后端的日界与小时分桶全部固定按 +8 小时计算（见 beijingDayRange、
 * usageSeriesBuckets 里的 strftime '+8 hours'），所以前端也必须按北京时区
 * 生成时间范围与展示时间，否则浏览器不在东八区时，KPI 与主图的窗口会错位。
 *
 * 实现方式：把时间戳加上 8 小时后用 UTC getter 读取日历字段。
 * 中国自 1991 年起不再使用夏令时，固定偏移是准确的。
 */

export const BJ_OFFSET = 8 * 60 * 60 * 1000

const pad = (n: number) => String(n).padStart(2, '0')

export type BjParts = {
  y: number
  m: number
  d: number
  h: number
  min: number
  s: number
  /** 0=周日 … 6=周六 */
  wd: number
}

/** 把毫秒时间戳拆成北京时间的日历字段。 */
export function bjParts(ms: number = Date.now()): BjParts {
  const d = new Date(ms + BJ_OFFSET)
  return {
    y: d.getUTCFullYear(),
    m: d.getUTCMonth() + 1,
    d: d.getUTCDate(),
    h: d.getUTCHours(),
    min: d.getUTCMinutes(),
    s: d.getUTCSeconds(),
    wd: d.getUTCDay(),
  }
}

/** 北京时间某自然日 0 点；offsetDays 可为负（昨天）或正（明天）。 */
export function bjDayStart(ms: number = Date.now(), offsetDays = 0): number {
  const p = bjParts(ms)
  return Date.UTC(p.y, p.m - 1, p.d + offsetDays) - BJ_OFFSET
}

/** 指定的北京日期（年、月 1-12、日）0 点。 */
export function bjDateStart(y: number, m: number, d: number): number {
  return Date.UTC(y, m - 1, d) - BJ_OFFSET
}

/** 北京时间所在小时的起点。 */
export function bjStartOfHour(ms: number = Date.now()): number {
  const p = bjParts(ms)
  return Date.UTC(p.y, p.m - 1, p.d, p.h) - BJ_OFFSET
}

/** 北京时间所在月份 1 号 0 点。 */
export function bjStartOfMonth(ms: number = Date.now()): number {
  const p = bjParts(ms)
  return Date.UTC(p.y, p.m - 1, 1) - BJ_OFFSET
}

/** 某年某月（1-12）的天数。 */
export function bjDaysInMonth(y: number, m: number): number {
  return new Date(Date.UTC(y, m, 0)).getUTCDate()
}

/** 某北京日期是星期几（0=周日）。 */
export function bjWeekday(y: number, m: number, d: number): number {
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay()
}

/** YYYY-MM-DD（北京）。 */
export function bjDateString(ms: number = Date.now()): string {
  const p = bjParts(ms)
  return `${p.y}-${pad(p.m)}-${pad(p.d)}`
}

/** YYYY-MM（北京），用于日历的月份游标。 */
export function bjMonthString(ms: number = Date.now()): string {
  const p = bjParts(ms)
  return `${p.y}-${pad(p.m)}`
}

/** YYYY-MM-DD HH:mm:ss（北京）。 */
export function bjFmtDateTime(ms: number): string {
  const p = bjParts(ms)
  return `${p.y}-${pad(p.m)}-${pad(p.d)} ${pad(p.h)}:${pad(p.min)}:${pad(p.s)}`
}

/** HH:mm:ss（北京），用于顶栏时钟。 */
export function bjFmtTime(ms: number = Date.now()): string {
  const p = bjParts(ms)
  return `${pad(p.h)}:${pad(p.min)}:${pad(p.s)}`
}

/** YYYY-MM-DD HH:00（北京），用于按小时的区间文案。 */
export function bjFmtHour(ms: number): string {
  const p = bjParts(ms)
  return `${p.y}-${pad(p.m)}-${pad(p.d)} ${pad(p.h)}:00`
}

/** MM-DD HH:mm:ss（北京），表格里省宽度用。 */
export function bjFmtShort(ms: number): string {
  const p = bjParts(ms)
  return `${pad(p.m)}-${pad(p.d)} ${pad(p.h)}:${pad(p.min)}:${pad(p.s)}`
}
