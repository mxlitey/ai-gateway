/**
 * EdgeOne Blob 存储适配层，实现与 Cloudflare KV 一致的接口。
 * 使用强一致读取（read-after-write），保证配置 / 错误日志等状态能读到最新值。
 *
 * Interface:
 *   await kv.get(key, 'json') -> parsed object or null
 *   await kv.get(key)         -> string or null
 *   await kv.put(key, value)  -> void
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
}
