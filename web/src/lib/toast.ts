/** 极简 toast：模块级事件总线 + 一个挂载在根部的渲染组件。 */

export type ToastKind = 'ok' | 'err'
type Item = { id: number; kind: ToastKind; text: string }

let seq = 0
let listeners: ((item: Item) => void)[] = []

export function toast(kind: ToastKind, text: string): void {
  const item: Item = { id: ++seq, kind, text }
  for (const l of listeners) l(item)
}

export function onToast(handler: (item: Item) => void): () => void {
  listeners.push(handler)
  return () => {
    listeners = listeners.filter((l) => l !== handler)
  }
}

export type { Item as ToastItem }