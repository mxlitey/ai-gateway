// 费用格式化：数值本身没有币种含义，符号由面板「货币符号」决定（可为空）。
// 单独放在这里，用量、仪表盘、价格表共用同一套小数位规则。

let cachedSymbol = ''

/** setCurrencySymbol 由设置页在读到运行期设置后调用，之后所有格式化都用它。 */
export function setCurrencySymbol(symbol: string) {
  cachedSymbol = symbol ?? ''
}

export function currencySymbol(): string {
  return cachedSymbol
}

/** fmtCost 格式化费用：0 显示 0，小额保留更多小数位。 */
export function fmtCost(n: number | undefined): string {
  const v = n ?? 0
  const s = cachedSymbol
  if (v === 0) return `${s}0`
  if (v < 0.01) return `${s}${v.toFixed(4)}`
  if (v < 1) return `${s}${v.toFixed(3)}`
  return `${s}${v.toFixed(2)}`
}

/** fmtPrice 格式化单价输入框里的数字：去掉浮点误差尾巴与多余的 0。 */
export function fmtPrice(n: number | undefined): string {
  if (!n || !Number.isFinite(n)) return ''
  return String(Number(n.toFixed(6)))
}
