// 其余所有路径的统一入口（/v1/* 代理、/admin/* 管理面板）
import { handleRequest } from './_handler.js';

export function onRequest(context) {
  return handleRequest(context);
}
