import { createStore } from './store/kv.js';
import { handleLogin, requireAuth } from './admin/auth.js';
import { handleAdminApi } from './admin/api.js';
import { getAdminPage } from './admin/page.js';
import { handleProxy } from './proxy/handler.js';

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const path = url.pathname;

    // CORS preflight
    if (request.method === 'OPTIONS') {
      return new Response(null, {
        headers: {
          'Access-Control-Allow-Origin': '*',
          'Access-Control-Allow-Methods': 'GET, POST, PUT, PATCH, DELETE, OPTIONS',
          'Access-Control-Allow-Headers': 'Content-Type, Authorization, x-api-key, anthropic-version',
          'Access-Control-Max-Age': '86400',
        },
      });
    }

    const store = createStore(env.KV);

    try {
      // ---- Admin routes ----

      // Admin SPA page
      if (path === '/admin' || path === '/admin/') {
        return new Response(getAdminPage(), {
          headers: { 'Content-Type': 'text/html; charset=utf-8' },
        });
      }

      // Admin login (no auth required)
      if (path === '/admin/api/login' && request.method === 'POST') {
        return handleLogin(request, env, store);
      }

      // Admin API (auth required)
      if (path.startsWith('/admin/api/')) {
        const authed = await requireAuth(request, env, store);
        if (!authed) {
          return new Response(JSON.stringify({ error: 'Unauthorized' }), {
            status: 401,
            headers: { 'Content-Type': 'application/json' },
          });
        }
        return handleAdminApi(request, env, store);
      }

      // ---- Proxy routes ----
      if (path.startsWith('/v1/')) {
        return handleProxy(request, env, store);
      }

      // ---- Health check ----
      if (path === '/' || path === '/health') {
        return new Response('AI Gateway is running.', {
          headers: { 'Content-Type': 'text/plain' },
        });
      }

      return new Response('Not Found', { status: 404 });
    } catch (err) {
      console.error('Unhandled error:', err);
      return new Response(JSON.stringify({ error: 'Internal Server Error' }), {
        status: 500,
        headers: { 'Content-Type': 'application/json' },
      });
    }
  },
};
