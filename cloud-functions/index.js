// 根路径入口：GET / 与 /health（健康检查）
import { handleRequest } from './_handler.js';

export function onRequest(context) {
  return handleRequest(context);
}
