/**
 * EdgeOne Blob 存储适配层，实现与 Cloudflare KV 一致的接口。
 * 使用强一致读取（read-after-write），保证配置 / 错误日志等状态能读到最新值。
 *
 * Interface:
 *   await kv.get(key, 'json') -> parsed object or null
 *   await kv.get(key)         -> string or null
 *   await kv.put(key, value)  -> void
 *   await kv.delete(key)      -> void
 *   await kv.list(prefix)     -> string[]（key 列表）
 */

import { getStore } from '@edgeone/pages-blob';

const DEFAULT_STORE = 'ai-gateway';

export class BlobKV {
  constructor() {
    this.store = getStore({ name: DEFAULT_STORE, consistency: 'strong' });
  }

  async get(key, type) {
    if (type === 'json') {
      return await this.store.get(key, { type: 'json', consistency: 'strong' });
    }
    return await this.store.get(key, { consistency: 'strong' });
  }

  async put(key, value) {
    await this.store.set(key, value);
  }

  async delete(key) {
    await this.store.delete(key);
  }

  /** 列出指定前缀下所有 key（SDK 默认自动聚合分页） */
  async list(prefix = '') {
    const { blobs } = await this.store.list({ prefix, consistency: 'strong' });
    return (blobs || []).map(b => (typeof b === 'string' ? b : b.key)).filter(Boolean);
  }
}
