export function getAdminPage() {
  return `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>AI Gateway</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{
  --bg-0:#09090b;--bg-1:#0f0f13;--bg-2:#18181b;--bg-3:#27272a;
  --bg-hover:#1f1f25;--text-0:#fafafa;--text-1:#a1a1aa;--text-2:#71717a;
  --border:#27272a;--primary:#6366f1;--primary-hover:#818cf8;
  --success:#22c55e;--danger:#ef4444;--danger-hover:#dc2626;--warning:#f59e0b;
  --radius:8px;
}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:var(--bg-0);color:var(--text-0);min-height:100vh}
a{color:inherit;text-decoration:none}
::-webkit-scrollbar{width:6px;height:6px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--bg-3);border-radius:3px}

/* Login */
.login-view{display:flex;align-items:center;justify-content:center;min-height:100vh;background:var(--bg-0)}
.login-card{background:var(--bg-2);border:1px solid var(--border);border-radius:16px;padding:48px;width:420px;text-align:center}
.login-logo{font-size:32px;font-weight:700;background:linear-gradient(135deg,#6366f1,#a78bfa);-webkit-background-clip:text;-webkit-text-fill-color:transparent;margin-bottom:8px}
.login-sub{color:var(--text-2);margin-bottom:28px;font-size:14px}
.login-card input{margin-bottom:16px}

/* Layout — 顶部导航栏（全设备统一，竖屏/横屏手机、桌面皆同） */
.main-view{flex-direction:column;min-height:100vh}
.sidebar{width:100%;background:var(--bg-1);border-right:none;border-bottom:1px solid var(--border);display:flex;flex-direction:column;position:fixed;top:0;left:0;right:0;z-index:50}
.sidebar-header{display:flex;align-items:center;justify-content:space-between;padding:12px 16px;border-bottom:1px solid var(--border)}
.sidebar-header .logo{font-size:20px;font-weight:700;background:linear-gradient(135deg,#6366f1,#a78bfa);-webkit-background-clip:text;-webkit-text-fill-color:transparent}
.sidebar-nav{display:flex;flex-direction:row;gap:4px;overflow-x:auto;padding:6px 8px;-webkit-overflow-scrolling:touch;scrollbar-width:none}
.sidebar-nav::-webkit-scrollbar{display:none}
.nav-item{display:block;padding:8px 12px;color:var(--text-1);border-radius:var(--radius);margin-bottom:0;cursor:pointer;transition:all .15s;font-size:14px;font-weight:500;white-space:nowrap;flex:0 0 auto}
.nav-item:hover{background:var(--bg-hover);color:var(--text-0)}
.nav-item.active{background:var(--primary);color:#fff}
.logout-icon{width:34px;height:34px;display:inline-flex;align-items:center;justify-content:center;border:none;background:transparent;color:var(--text-1);border-radius:8px;cursor:pointer;transition:all .15s}
.logout-icon:hover{background:var(--bg-hover);color:var(--danger)}
.content{flex:1;margin-left:0;padding:128px 16px 32px;max-width:none}

/* Section header */
.section-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px}
.section-header h2{font-size:24px;font-weight:600}

/* Cards */
.stats-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:16px;margin-bottom:28px}
.stat-card{background:var(--bg-2);border:1px solid var(--border);border-radius:var(--radius);padding:24px}
.stat-card .label{color:var(--text-2);font-size:13px;margin-bottom:8px;text-transform:uppercase;letter-spacing:.5px}
.stat-card .value{font-size:36px;font-weight:700}
.info-card{background:var(--bg-2);border:1px solid var(--border);border-radius:var(--radius);padding:24px}
.info-card h3{margin-bottom:12px;font-size:16px;font-weight:600}
.info-card p{color:var(--text-1);font-size:14px;line-height:2}
.info-card code{background:var(--bg-1);padding:2px 8px;border-radius:4px;font-family:'SF Mono',SFMono-Regular,Consolas,monospace;font-size:13px;color:var(--primary)}

/* Table */
.table-container{background:var(--bg-2);border:1px solid var(--border);border-radius:var(--radius);overflow-x:auto}
table{width:100%;border-collapse:collapse;min-width:700px}
th{text-align:left;padding:12px 16px;background:var(--bg-3);font-size:12px;color:var(--text-2);font-weight:600;text-transform:uppercase;letter-spacing:.5px;white-space:nowrap}
td{padding:12px 16px;border-top:1px solid var(--border);font-size:14px;vertical-align:middle}
tr:hover td{background:var(--bg-hover)}
.cell-truncate{max-width:240px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
/* 错误日志：单行省略 + 三角展开 + 一键复制 */
.err-wrap{display:flex;align-items:flex-start;gap:6px;min-width:220px}
.err-caret{flex:0 0 auto;cursor:pointer;color:var(--text-2);font-size:11px;line-height:20px;transition:transform .15s;user-select:none}
.err-wrap.open .err-caret{transform:rotate(90deg)}
.err-text{flex:1 1 auto;min-width:0;font-size:13px;max-width:420px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;word-break:break-all}
.err-wrap.open .err-text{white-space:pre-wrap;max-width:none;word-break:break-all}
.err-copy{flex:0 0 auto;border:none;background:var(--bg-1);color:var(--text-1);width:24px;height:24px;border-radius:5px;cursor:pointer;line-height:1;font-size:13px}
.err-copy:hover{color:var(--primary);background:var(--bg-3)}

/* Badge */
.badge{display:inline-block;padding:3px 10px;border-radius:9999px;font-size:12px;font-weight:500}
.badge-on{background:rgba(34,197,94,.12);color:var(--success)}
.badge-off{background:rgba(239,68,68,.12);color:var(--danger)}
.tag-direct{display:inline-block;padding:2px 8px;border-radius:9999px;font-size:11px;font-weight:500;background:rgba(99,102,241,.12);color:var(--primary)}

/* Buttons */
.btn{padding:8px 16px;border:none;border-radius:6px;cursor:pointer;font-size:14px;font-weight:500;transition:all .15s;display:inline-flex;align-items:center;gap:6px;line-height:1.4}
.btn:active{transform:scale(.97)}
.btn-primary{background:var(--primary);color:#fff}
.btn-primary:hover{background:var(--primary-hover)}
.btn-danger{background:transparent;color:var(--danger);border:1px solid rgba(239,68,68,.3)}
.btn-danger:hover{background:var(--danger);color:#fff}
.btn-ghost{background:transparent;color:var(--text-1);border:1px solid var(--border)}
.btn-ghost:hover{background:var(--bg-hover);color:var(--text-0)}
.btn-sm{padding:5px 10px;font-size:12px}
.btn-full{width:100%;justify-content:center}

/* Forms */
input,textarea,select{width:100%;padding:10px 14px;background:var(--bg-0);border:1px solid var(--border);border-radius:6px;color:var(--text-0);font-size:14px;font-family:inherit;outline:none;transition:border-color .15s}
input:focus,textarea:focus,select:focus{border-color:var(--primary)}
input::placeholder,textarea::placeholder{color:var(--text-2)}
textarea{resize:vertical;min-height:100px}
label{display:block;margin-bottom:6px;font-size:13px;color:var(--text-1);font-weight:500}
.form-group{margin-bottom:16px}
.form-row{display:grid;grid-template-columns:1fr 1fr;gap:16px}
.form-help{font-size:12px;color:var(--text-2);margin-top:4px}

/* Modal */
.modal-overlay{position:fixed;inset:0;background:rgba(0,0,0,.6);display:flex;align-items:center;justify-content:center;z-index:100;backdrop-filter:blur(2px)}
.modal{background:var(--bg-2);border:1px solid var(--border);border-radius:12px;padding:28px;width:560px;max-height:85vh;overflow-y:auto}
.modal h3{font-size:18px;margin-bottom:20px;font-weight:600}
.modal-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:24px}

/* Toast */
.toast-container{position:fixed;top:20px;right:20px;z-index:200}
.toast{background:var(--bg-3);border:1px solid var(--border);border-radius:var(--radius);padding:12px 20px;margin-bottom:8px;font-size:14px;animation:slideIn .3s ease;min-width:240px}
.toast.success{border-left:3px solid var(--success)}
.toast.error{border-left:3px solid var(--danger)}
@keyframes slideIn{from{transform:translateX(100%);opacity:0}to{transform:translateX(0);opacity:1}}

/* Key display */
.key-mono{font-family:'SF Mono',SFMono-Regular,Consolas,monospace;font-size:13px;background:var(--bg-0);padding:3px 8px;border-radius:4px;display:inline-block}

/* Empty state */
.empty{text-align:center;color:var(--text-2);padding:48px 20px;font-size:14px}

/* 渠道模型勾选 */
.model-picker{background:var(--bg-0);border:1px solid var(--border);border-radius:6px;padding:12px;max-height:220px;overflow-y:auto;margin-bottom:8px}
.date-picker{display:flex;gap:8px;align-items:center;margin-bottom:20px}
.date-picker input[type="date"]{width:180px}

/* 分页控件 */
.pagination{display:flex;align-items:center;justify-content:center;gap:4px;margin-top:14px;padding-top:14px;border-top:1px solid var(--border)}
.pagination button{min-width:32px;height:28px;border:1px solid var(--border);border-radius:4px;background:var(--bg-1);color:var(--text-1);font-size:12px;cursor:pointer;transition:all .15s;padding:0 8px}
.pagination button:hover:not(:disabled):not(.pg-active){background:var(--bg-3);border-color:var(--primary)}
.pagination button:disabled{opacity:.35;cursor:default}
.pagination .pg-active{background:var(--primary);color:#fff;border-color:var(--primary);font-weight:600}
.pagination .pg-info{font-size:12px;color:var(--text-2);margin:0 4px}

/* 路由多目标编辑 */
.rt-target-row{display:grid;grid-template-columns:1fr 1fr auto;gap:10px;align-items:end;background:var(--bg-1);border:1px solid var(--border);border-radius:8px;padding:12px;margin-bottom:10px}
.rt-target-row label{font-size:12px;margin-bottom:4px}
.rt-add-target{width:100%;margin-bottom:8px}

/* 渠道模型勾选 */
.model-picker{background:var(--bg-0);border:1px solid var(--border);border-radius:6px;padding:12px;max-height:220px;overflow-y:auto;margin-bottom:8px}
.model-picker label{display:flex;align-items:center;gap:8px;cursor:pointer;font-size:13px;margin-bottom:6px;color:var(--text-1);font-weight:400}
.model-picker input[type="checkbox"]{width:auto;flex:0 0 auto}
.model-picker-empty{color:var(--text-2);font-size:13px;padding:4px 0}

/* 公开模型 → 上游模型 映射编辑 */
.map-row{position:relative;display:flex;align-items:center;gap:8px;background:var(--bg-1);border:1px solid var(--border);border-radius:8px;padding:8px 10px;margin-bottom:8px}
.map-row input{flex:1;min-width:0}
.map-up-wrap{position:relative;flex:1;min-width:0;display:flex}
.map-up-wrap .map-up{width:100%;flex:1}
.map-up-listbox{position:absolute;top:calc(100% + 4px);left:0;right:0;z-index:120;background:var(--bg-2);border:1px solid var(--border);border-radius:8px;max-height:220px;overflow-y:auto;box-shadow:0 8px 24px rgba(0,0,0,.4)}
.map-up-item{padding:8px 10px;font-size:13px;color:var(--text-0);cursor:pointer;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.map-up-item:hover,.map-up-item.active{background:var(--primary);color:#fff}
.map-up-empty{padding:10px;font-size:13px;color:var(--text-2);text-align:center}
.map-arrow{color:var(--text-2);font-size:14px;flex:0 0 auto}
.map-del{flex:0 0 auto;width:32px;height:32px;padding:0;display:inline-flex;align-items:center;justify-content:center;border:1px solid rgba(239,68,68,.3);color:var(--danger);background:transparent;border-radius:6px;cursor:pointer}
.map-del:hover{background:var(--danger);color:#fff}
.key-row{display:flex;align-items:center;gap:8px;background:var(--bg-1);border:1px solid var(--border);border-radius:8px;padding:8px 10px;margin-bottom:8px}
.key-row input.key-val{flex:1;min-width:0}
.key-row.key-disabled{opacity:.5}
.key-switch{position:relative;flex:0 0 auto;width:36px;height:20px}
.key-switch input{position:absolute;opacity:0;width:0;height:0}
.key-slider{position:relative;display:block;width:36px;height:20px;background:var(--border);border-radius:10px;cursor:pointer;transition:background .2s}
.key-slider::after{content:'';position:absolute;top:2px;left:2px;width:16px;height:16px;background:#fff;border-radius:50%;transition:transform .2s}
.key-switch input:checked + .key-slider{background:var(--success)}
.key-switch input:checked + .key-slider::after{transform:translateX(16px)}

/* 竖屏小屏补充适配 */
@media (max-width: 640px){
  .content{padding-top:124px;padding-left:12px;padding-right:12px}
  .section-header{flex-direction:column;align-items:stretch;gap:10px;margin-bottom:16px}
  .section-header h2{font-size:20px}
  .stats-grid{grid-template-columns:1fr 1fr;gap:10px}
  .stat-card{padding:16px}
  .stat-card .value{font-size:26px}
  .form-row{grid-template-columns:1fr;gap:0}
  .modal{width:92%;max-width:560px;margin:0;border-radius:12px;padding:20px;max-height:90vh}
  .modal-overlay{align-items:center}
  .login-card{width:100%;max-width:92vw;padding:32px 24px}
  .info-card{padding:16px}
  .toast-container{top:auto;right:12px;left:12px;bottom:12px}
  .toast{min-width:0;width:100%}
  .date-picker{flex-wrap:wrap}
  .date-picker input[type="date"]{width:100%}
  .rt-target-row{grid-template-columns:1fr 1fr;gap:8px}
  .rt-trg-del{grid-column:span 2}
  .map-row{flex-wrap:wrap}
  .map-row .map-arrow{display:none}
  .map-row input{flex:1 1 45%}
  .map-del{flex:1 0 auto;margin-top:4px}
  .key-row{flex-wrap:wrap}
  .key-row input.key-val{flex:1 1 45%}
}
</style>
</head>
<body>
<div id="app">

<!-- ====== Login ====== -->
<div id="login-view" class="login-view">
  <div class="login-card">
    <div class="login-logo">AI Gateway</div>
    <p class="login-sub" id="login-sub"></p>
    <input type="password" id="login-pwd" autofocus>
    <button class="btn btn-primary btn-full" onclick="doLogin()" id="login-btn"></button>
  </div>
</div>

<!-- ====== Main ====== -->
<div id="main-view" class="main-view" style="display:none">
  <aside class="sidebar">
    <div class="sidebar-header">
      <div class="logo">AI Gateway</div>
      <button class="logout-icon" onclick="doLogout()" title="退出登录" aria-label="退出登录">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
      </button>
    </div>
    <nav class="sidebar-nav" id="sidebar-nav">
      <a class="nav-item active" data-section="dashboard" onclick="navigate('dashboard')"></a>
      <a class="nav-item" data-section="channels" onclick="navigate('channels')"></a>
      <a class="nav-item" data-section="routes" onclick="navigate('routes')"></a>
      <a class="nav-item" data-section="apikeys" onclick="navigate('apikeys')"></a>
      <a class="nav-item" data-section="errors" onclick="navigate('errors')"></a>
    </nav>
  </aside>

  <main class="content">
    <!-- Dashboard -->
    <section id="section-dashboard" class="section">
      <div class="section-header"><h2 id="dash-title"></h2></div>
      <div class="stats-grid">
        <div class="stat-card"><div class="label" id="lbl-ch"></div><div class="value" id="s-ch">0</div></div>
        <div class="stat-card"><div class="label" id="lbl-en"></div><div class="value" id="s-en">0</div></div>
        <div class="stat-card"><div class="label" id="lbl-uk"></div><div class="value" id="s-uk">0</div></div>
        <div class="stat-card"><div class="label" id="lbl-ak"></div><div class="value" id="s-ak">0</div></div>
      </div>
      <div class="info-card" id="info-card"></div>
    </section>

    <!-- Channels -->
    <section id="section-channels" class="section" style="display:none">
      <div class="section-header">
        <h2 id="ch-title"></h2>
        <button class="btn btn-primary" onclick="showChModal()" id="ch-add-btn"></button>
      </div>
      <div class="table-container">
        <table>
          <thead><tr id="ch-thead"></tr></thead>
          <tbody id="ch-tbody"></tbody>
        </table>
      </div>
    </section>

    <!-- Model Routes (只读展示) -->
    <section id="section-routes" class="section" style="display:none">
      <div class="section-header">
        <h2 id="routes-title"></h2>
      </div>
      <div class="info-card" id="routes-info"></div>
      <div class="table-container" style="margin-top:16px">
        <table>
          <thead><tr id="routes-thead"></tr></thead>
          <tbody id="routes-tbody"></tbody>
        </table>
      </div>
    </section>

    <!-- Error Logs -->
    <section id="section-errors" class="section" style="display:none">
      <div class="section-header">
        <h2 id="error-title"></h2>
        <button class="btn btn-ghost" onclick="loadErrors()" id="error-refresh-btn"></button>
      </div>
      <div class="date-picker">
        <label id="error-date-label" style="margin:0;white-space:nowrap"></label>
        <input type="date" id="error-date" onchange="loadErrors()">
      </div>
      <div id="error-container"></div>
    </section>

    <!-- API Keys -->
    <section id="section-apikeys" class="section" style="display:none">
      <div class="section-header">
        <h2 id="ak-title"></h2>
        <button class="btn btn-primary" onclick="showAkModal()" id="ak-gen-btn"></button>
      </div>
      <div class="table-container">
        <table>
          <thead><tr id="ak-thead"></tr></thead>
          <tbody id="ak-tbody"></tbody>
        </table>
      </div>
    </section>
  </main>
</div>

<!-- Modal -->
<div id="modal-overlay" class="modal-overlay" style="display:none">
  <div class="modal" id="modal-box"></div>
</div>

<!-- Toast -->
<div id="toast-container" class="toast-container"></div>

</div>
<script>
// ============ i18n ============
const I18N = {
  en: {
    loginSub: 'Enter admin password or API key to continue',
    loginPlaceholder: 'Password or API Key',
    signIn: 'Sign In',
    signOut: 'Sign Out',
    dashboard: 'Dashboard',
    channels: 'Channels',
    apiKeys: 'API Keys',
    totalChannels: 'Total Channels',
    enabled: 'Enabled',
    upstreamKeys: 'Upstream Keys',
    clientApiKeys: 'Client API Keys',
    quickStart: 'Quick Start',
    qs1: '1. Go to <strong>Channels</strong> and add an upstream API service with keys',
    qs2: '2. Go to <strong>API Keys</strong> and generate a client key',
    qs3: '3. Point your AI client to one of the endpoints below:',
    openaiFormat: 'OpenAI format:',
    claudeFormat: 'Claude format:',
    claudeHelp: 'Claude endpoint accepts <code>x-api-key</code> or <code>Authorization: Bearer</code> header for authentication.',
    addChannel: 'Add Channel',
    editChannel: 'Edit Channel',
    name: 'Name',
    baseUrl: 'API Host',
    pathLabel: 'API Path (optional)',
    keys: 'Keys',
    models: 'Models',
    priority: 'Priority',
    weight: 'Weight',
    status: 'Status',
    actions: 'Actions',
    edit: 'Edit',
    disable: 'Disable',
    enable: 'Enable',
    delete: 'Delete',
    on: 'On',
    off: 'Off',
    all: 'All',
    noChannels: 'No channels yet. Click "Add Channel" to get started.',
    namePlaceholder: 'e.g. NVIDIA NIM',
    urlPlaceholder: 'e.g. https://integrate.api.nvidia.com/v1',
    keysLabel: 'API Keys',
    addKey: 'Add Key',
    modelsLabel: 'Models',
    modelsPlaceholder: 'gpt-4o',
    modelsHelp: 'Only requests for these models will route to this channel. Leave empty to accept any model.',
    priorityHelp: 'Lower = higher priority. Tried first.',
    weightHelp: 'Relative weight within same priority group.',
    cancel: 'Cancel',
    save: 'Save',
    generateKey: 'Generate Key',
    key: 'Key',
    created: 'Created',
    noApiKeys: 'No API keys. Click "Generate Key" to create one.',
    copy: 'Copy',
    genKeyTitle: 'Generate API Key',
    nameOptional: 'Name (optional)',
    nameOptPlaceholder: 'e.g. My App',
    generate: 'Generate',
    keyCreated: 'API Key Created',
    keyCreatedHint: 'Copy this key now. It will be masked after you close this dialog.',
    copyClose: 'Copy & Close',
    confirmDelete: 'Confirm Delete',
    confirmDeleteMsg: 'Are you sure you want to delete <strong>{name}</strong>? This cannot be undone.',
    deleted: 'Deleted',
    sessionExpired: 'Session expired',
    networkError: 'Network error',
    enterPwd: 'Please enter password',
    loginFailed: 'Login failed',
    channelUpdated: 'Channel updated',
    channelCreated: 'Channel created',
    nameUrlRequired: 'Name and Base URL are required',
    saveFailed: 'Save failed',
    failed: 'Failed',
    copied: 'Copied!',
    copyFailed: 'Copy failed',
    boundChannels: 'Bound Channels',
    allChannels: 'All Channels',
    selectChannels: 'Select Channels',
    channelBindHelp: 'Only route to selected channels. Empty = all channels.',
    editKey: 'Edit Key',
    errorLogs: 'Error Logs',
    logs: 'Logs',
    errorDate: 'Date',
    refreshError: 'Refresh',
    noErrors: 'No errors today.',
    errorTime: 'Time',
    errorModel: 'Model',
    errorStatus: 'Status',
    errorMessage: 'Message',
    errorKey: 'Key',
    copyMsg: 'Copy',
    expandMsg: 'Expand',
    copied: 'Copied',
    copyFail: 'Copy failed',
    errorsToday: 'errors today',
    modelRoutes: 'Model Routes',
    direct: 'Passthrough',
    addRoute: 'Add Route',
    editRoute: 'Edit Route',
    routeName: 'Name (Alias)',
    publicModel: 'Public Model',
    targetChannel: 'Target Channel',
    upstreamModel: 'Upstream Model',
    publicModelHelp: 'The model name clients will use. Must match the model in requests.',
    upstreamModelHelp: 'The actual model name sent to the upstream channel. Defaults to the public model.',
    noRoutes: 'No routes yet. Click "Add Route" to create one.',
    routeCreated: 'Route created',
    routeUpdated: 'Route updated',
    routeModelRequired: 'Public model and Target channel are required',
    selectChannel: 'Select a channel',
    routesInfo: 'Model routing paths across all enabled channels (try in channel order, keys rotate with random start). Edit mappings in Channels.',
    routesNone: 'No model routes yet. Configure a "public model → upstream model" mapping in Channels.',
    modelMapLabel: 'Public Model → Upstream Model',
    addMapping: 'Add Mapping',
    mapPublicModelPh: 'Public model',
    mapUpstreamPh: 'Upstream model',
  },
  zh: {
    loginSub: '请输入管理员密码或 API Key 继续',
    loginPlaceholder: '密码或 API Key',
    signIn: '登录',
    signOut: '退出登录',
    dashboard: '仪表盘',
    channels: '渠道管理',
    apiKeys: 'API 密钥',
    totalChannels: '渠道总数',
    enabled: '已启用',
    upstreamKeys: '上游密钥',
    clientApiKeys: '客户端密钥',
    quickStart: '快速开始',
    qs1: '1. 前往 <strong>渠道管理</strong>，添加上游 API 服务和密钥',
    qs2: '2. 前往 <strong>API 密钥</strong>，生成客户端密钥',
    qs3: '3. 将 AI 客户端指向以下端点：',
    openaiFormat: 'OpenAI 格式：',
    claudeFormat: 'Claude 格式：',
    claudeHelp: 'Claude 端点支持 <code>x-api-key</code> 或 <code>Authorization: Bearer</code> 请求头进行认证。',
    addChannel: '添加渠道',
    editChannel: '编辑渠道',
    name: '名称',
    baseUrl: 'API 主机',
    pathLabel: 'API 路径',
    keysLabel: 'API 密钥',
    addKey: '新增密钥',
    models: '模型',
    status: '状态',
    actions: '操作',
    edit: '编辑',
    disable: '禁用',
    enable: '启用',
    delete: '删除',
    on: '启用',
    off: '停用',
    all: '全部',
    noChannels: '暂无渠道，点击「添加渠道」开始配置。',
    namePlaceholder: '例如 NVIDIA NIM',
    urlPlaceholder: '例如 https://integrate.api.nvidia.com/v1',
    modelsLabel: '模型列表',
    modelsPlaceholder: 'gpt-4o',
    modelsHelp: '无映射的模型，请求同名模型时直接透传到此渠道。留空则无同名透传，仅映射生效。',
    cancel: '取消',
    save: '保存',
    generateKey: '生成密钥',
    key: '密钥',
    created: '创建时间',
    noApiKeys: '暂无 API 密钥，点击「生成密钥」创建。',
    copy: '复制',
    genKeyTitle: '生成 API 密钥',
    nameOptional: '名称（可选）',
    nameOptPlaceholder: '例如 我的应用',
    generate: '生成',
    keyCreated: 'API 密钥已创建',
    keyCreatedHint: '请立即复制此密钥，关闭对话框后将不再显示完整密钥。',
    copyClose: '复制并关闭',
    confirmDelete: '确认删除',
    confirmDeleteMsg: '确定要删除 <strong>{name}</strong> 吗？此操作不可撤销。',
    deleted: '已删除',
    sessionExpired: '会话已过期',
    networkError: '网络错误',
    enterPwd: '请输入密码',
    loginFailed: '登录失败',
    channelUpdated: '渠道已更新',
    channelCreated: '渠道已创建',
    nameUrlRequired: '名称和基础 URL 不能为空',
    saveFailed: '保存失败',
    failed: '操作失败',
    copied: '已复制！',
    copyFailed: '复制失败',
    boundChannels: '绑定渠道',
    allChannels: '全部渠道',
    selectChannels: '选择渠道',
    channelBindHelp: '仅路由到选中的渠道。不选则使用全部渠道。',
    editKey: '编辑密钥',
    errorLogs: '错误日志',
    logs: '日志',
    errorDate: '日期',
    refreshError: '刷新',
    noErrors: '今日暂无错误。',
    errorTime: '时间',
    errorModel: '模型',
    errorStatus: '状态码',
    errorMessage: '错误信息',
    errorKey: '密钥',
    copyMsg: '复制',
    expandMsg: '展开',
    copied: '已复制',
    copyFail: '复制失败',
    errorsToday: '个错误',
    modelRoutes: '模型路由',
    direct: '透传',
    addRoute: '添加路由',
    editRoute: '编辑路由',
    routeName: '名称（别名）',
    publicModel: '公开模型名',
    targetChannel: '目标渠道',
    upstreamModel: '上游模型',
    publicModelHelp: '客户端使用的模型名，需与请求中的 model 一致。',
    upstreamModelHelp: '实际转发给上游渠道的模型名，默认等于公开模型名。',
    noRoutes: '暂无路由，点击「添加路由」创建。',
    routeCreated: '路由已创建',
    routeUpdated: '路由已更新',
    routeModelRequired: '公开模型名不能为空',
    selectChannel: '选择一个渠道',
    addTarget: '添加目标渠道',
    removeTarget: '移除',
    atLeastOneTarget: '公开模型名不能为空，且至少关联一个上游渠道模型',
    fetchModels: '获取上游模型',
    fetchingModels: '获取中…',
    modelPickerHelp: '勾选模型即添加到该渠道；也可手动在下方输入。',
    modelPickerEmpty: '未获取到模型，请检查基础 URL 与密钥。',
    routeTargetCol: '目标渠道 / 上游模型',
    modelSearchPlaceholder: '搜索模型…',
    modelSearchEmpty: '未找到匹配的模型。',
    selectUpstream: '请选择上游模型',
    routesInfo: '以下为各公开模型在当前所有启用渠道中的路由路径（按渠道存储顺序尝试，渠道内密钥随机起点轮换）。如需调整映射，请前往「渠道管理」编辑。',
    routesNone: '暂无任何模型路由。请在「渠道管理」中为渠道配置「公开模型 → 上游模型」映射。',
    modelMapLabel: '公开模型 → 上游模型　映射',
    addMapping: '添加映射',
    mapPublicModelPh: '公开模型名',
    mapUpstreamPh: '上游模型名',
    noMatchingModels: '无匹配的上游模型',
  },
};

// 项目仅保留中文界面
const lang = 'zh';
function t(key) { return I18N[lang]?.[key] || key; }

// ============ State ============
let token = localStorage.getItem('ag_token');
let channels = [];
let apiKeys = [];
let curSection = 'dashboard';

// 渠道弹窗中"获取上游模型"拉取到的模型缓存（用于勾选列表 + 搜索过滤）
let lastFetchedModels = [];

// 渠道弹窗中「公开模型 → 上游模型」映射（用于编辑，未保存前暂存于此）
let modelMapRows = [];

// 渠道弹窗中的 API 密钥列表（{ key, enabled }，未保存前暂存于此）
let keyRows = [];

// ============ API ============
async function api(path, opts = {}) {
  try {
    const res = await fetch('/admin/api' + path, {
      ...opts,
      headers: {
        'Content-Type': 'application/json',
        'Authorization': 'Bearer ' + token,
        ...(opts.headers || {}),
      },
    });
    if (res.status === 401) { doLogout(); toast(t('sessionExpired'), 'error'); return null; }
    return await res.json();
  } catch (e) {
    toast(t('networkError') + ': ' + e.message, 'error');
    return null;
  }
}

async function loadData() {
  const [ch, ak] = await Promise.all([
    api('/channels'),
    api('/apikeys'),
  ]);
  channels = ch || [];
  apiKeys = ak || [];
}

// ============ Auth ============
async function doLogin() {
  const pwd = document.getElementById('login-pwd').value;
  if (!pwd) { toast(t('enterPwd'), 'error'); return; }
  try {
    const res = await fetch('/admin/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: pwd }),
    });
    const data = await res.json();
    if (res.ok && data.token) {
      token = data.token;
      localStorage.setItem('ag_token', token);
      showMain();
      await loadData();
      render();
    } else {
      toast(data.error || t('loginFailed'), 'error');
    }
  } catch (e) {
    toast(t('networkError'), 'error');
  }
}

function doLogout() {
  token = null;
  localStorage.removeItem('ag_token');
  showLogin();
}

// ============ Views ============
function showLogin() {
  document.getElementById('login-view').style.display = 'flex';
  document.getElementById('main-view').style.display = 'none';
  renderLogin();
}

function showMain() {
  document.getElementById('login-view').style.display = 'none';
  document.getElementById('main-view').style.display = 'flex';
  renderSidebar();
}

function renderLogin() {
  document.getElementById('login-sub').textContent = t('loginSub');
  document.getElementById('login-pwd').placeholder = t('loginPlaceholder');
  document.getElementById('login-btn').textContent = t('signIn');
}

function renderSidebar() {
  const navMap = { dashboard: 'dashboard', channels: 'channels', routes: 'modelRoutes', apikeys: 'apiKeys', errors: 'logs' };
  document.querySelectorAll('#sidebar-nav .nav-item').forEach(el => {
    el.textContent = t(navMap[el.dataset.section]);
    el.classList.toggle('active', el.dataset.section === curSection);
  });
}

function navigate(section) {
  curSection = section;
  document.querySelectorAll('.nav-item').forEach(el => {
    el.classList.toggle('active', el.dataset.section === section);
  });
  document.querySelectorAll('.section').forEach(el => {
    el.style.display = el.id === 'section-' + section ? 'block' : 'none';
  });
  render();
}

function render() {
  renderDashboard();
  renderChannelHeaders();
  renderApiKeyHeaders();
  if (curSection === 'channels') renderChannels();
  if (curSection === 'routes') renderRoutes();
  if (curSection === 'errors') { renderErrorHeaders(); loadErrors(); }
  if (curSection === 'apikeys') renderApiKeys();
}

function renderChannelHeaders() {
  document.getElementById('ch-title').textContent = t('channels');
  document.getElementById('ch-add-btn').textContent = t('addChannel');
  document.getElementById('ch-thead').innerHTML = '<th>'+[t('name'),t('baseUrl'),t('keys'),t('models'),t('status'),t('actions')].join('</th><th>')+'</th>';
}

function renderApiKeyHeaders() {
  document.getElementById('ak-title').textContent = t('apiKeys');
  document.getElementById('ak-gen-btn').textContent = t('generateKey');
  document.getElementById('ak-thead').innerHTML = '<th>'+[t('name'),t('key'),t('boundChannels'),t('created'),t('status'),t('actions')].join('</th><th>')+'</th>';
}

// ============ Dashboard ============
function renderDashboard() {
  document.getElementById('dash-title').textContent = t('dashboard');
  document.getElementById('lbl-ch').textContent = t('totalChannels');
  document.getElementById('lbl-en').textContent = t('enabled');
  document.getElementById('lbl-uk').textContent = t('upstreamKeys');
  document.getElementById('lbl-ak').textContent = t('clientApiKeys');
  const en = channels.filter(c => c.enabled).length;
  const uk = channels.reduce((s, c) => s + (c.keys?.length || 0), 0);
  document.getElementById('s-ch').textContent = channels.length;
  document.getElementById('s-en').textContent = en;
  document.getElementById('s-uk').textContent = uk;
  document.getElementById('s-ak').textContent = apiKeys.length;
  const baseUrl = location.origin;
  document.getElementById('info-card').innerHTML = \`
    <h3>\${t('quickStart')}</h3>
    <p>\${t('qs1')}</p>
    <p>\${t('qs2')}</p>
    <p>\${t('qs3')}</p>
    <p style="margin-top:8px"><strong>\${t('openaiFormat')}</strong> <code>\${baseUrl}/v1</code></p>
    <p><strong>\${t('claudeFormat')}</strong> <code>\${baseUrl}/v1/messages</code></p>
    <div class="form-help" style="margin-top:8px">\${t('claudeHelp')}</div>
  \`;
}

// ============ Channels ============
function renderChannels() {
  const tb = document.getElementById('ch-tbody');
  if (!channels.length) {
    tb.innerHTML = '<tr><td colspan="6" class="empty">' + t('noChannels') + '</td></tr>';
    return;
  }
  tb.innerHTML = channels.map(c => {
    const mn = channelModelNames(c);
    const modelCell = mn.length
      ? mn.length
      : '<span style="color:var(--text-2)">' + t('all') + '</span>';
    return \`
    <tr>
      <td><strong>\${esc(c.name)}</strong></td>
      <td class="cell-truncate" title="\${esc(c.base_url + (c.path || '/chat/completions'))}">\${esc(c.base_url)}\${c.path ? ' <span style="color:var(--text-2);font-size:12px">' + esc(c.path) + '</span>' : ''}</td>
      <td>\${c.keys?.length || 0}</td>
      <td class="cell-truncate" title="\${esc(mn.join(', '))}">\${modelCell}</td>
      <td><span class="badge \${c.enabled ? 'badge-on' : 'badge-off'}">\${c.enabled ? t('on') : t('off')}</span></td>
      <td style="white-space:nowrap">
        <button class="btn btn-sm btn-ghost" onclick="showChModal('\${c.id}')">\${t('edit')}</button>
        <button class="btn btn-sm btn-ghost" onclick="toggleCh('\${c.id}')">\${c.enabled ? t('disable') : t('enable')}</button>
        <button class="btn btn-sm btn-danger" onclick="confirmDel('channel','\${c.id}','\${esc(c.name)}')">\${t('delete')}</button>
      </td>
    </tr>
  \`}).join('');
}

// 合并渠道的「模型列表」与「模型映射」，按上游模型名去重后返回
function channelModelNames(c) {
  const names = new Set();
  (c.models || []).forEach(m => { if (m) names.add(String(m)); });
  if (c.model_map && typeof c.model_map === 'object') {
    Object.keys(c.model_map).forEach(p => { const u = c.model_map[p]; if (u) names.add(String(u)); });
  }
  return Array.from(names);
}

function showChModal(id) {
  const ch = id ? channels.find(c => c.id === id) : null;
  const title = ch ? t('editChannel') : t('addChannel');
  const html = \`
    <h3>\${title}</h3>
    <input type="hidden" id="f-ch-id" value="\${id || ''}">
    <div class="form-group">
      <label>\${t('name')}</label>
      <input id="f-name" value="\${ch ? esc(ch.name) : ''}" placeholder="\${t('namePlaceholder')}">
    </div>
    <div class="form-group">
      <label>\${t('baseUrl')}</label>
      <input id="f-url" value="\${ch ? esc(ch.base_url) : ''}" placeholder="\${t('urlPlaceholder')}">
    </div>
    <div class="form-group">
      <label>\${t('pathLabel')}</label>
      <input id="f-path" value="\${ch ? esc(ch.path || '') : ''}" placeholder="/chat/completions">
    </div>
    <div class="form-group">
      <label>\${t('keysLabel')}</label>
      <div class="key-list" id="f-keys-list"></div>
      <button type="button" class="btn btn-sm btn-ghost" style="margin-top:4px;width:100%" onclick="addKeyRow()">+ \${t('addKey')}</button>
    </div>
    <div class="form-group">
      <label>\${t('modelsLabel')}</label>
      <button type="button" class="btn btn-sm btn-ghost" style="margin-bottom:8px" onclick="fetchUpstreamChannelModels(this)">\${t('fetchModels')}</button>
      <input id="f-models-search" oninput="renderChannelModelPicker()" placeholder="\${t('modelSearchPlaceholder')}" style="margin-bottom:8px">
      <div class="model-picker" id="f-models-picker"><div class="model-picker-empty">\${t('modelPickerHelp')}</div></div>
      <textarea id="f-models" style="min-height:80px" placeholder="\${t('modelsPlaceholder')}">\${ch ? (ch.models||[]).join('\\n') : ''}</textarea>
    </div>
    <div class="form-group">
      <label>\${t('modelMapLabel')}</label>
      <div id="f-modelmap"></div>
      <button type="button" class="btn btn-sm btn-ghost" style="width:100%" onclick="addModelMapRow()">+ \${t('addMapping')}</button>
    </div>
    <div class="modal-actions">
      <button class="btn btn-ghost" onclick="closeModal()">\${t('cancel')}</button>
      <button class="btn btn-primary" onclick="saveCh('\${id||''}')">\${t('save')}</button>
    </div>
  \`;
  modelMapRows = ch && ch.model_map && typeof ch.model_map === 'object'
    ? Object.entries(ch.model_map).map(([p, u]) => ({ public: p, upstream: String(u) }))
    : [];
  keyRows = (ch ? ch.keys || [] : []).map(k =>
    typeof k === 'string' ? { key: k, enabled: true } : { key: String(k.key || ''), enabled: k.enabled !== false }
  );
  if (keyRows.length === 0) keyRows.push({ key: '', enabled: true });
  lastFetchedModels = [];
  mapUpCtx = { sig: '', list: [] };
  openModal(html);
  renderKeyRows();
  renderModelMapRows();
}

// 获取上游模型并渲染勾选列表（多选，勾选即加入该渠道模型）
async function fetchUpstreamChannelModels(btn) {
  const id = document.getElementById('f-ch-id').value;
  const keys = keyRows.filter(r => r.enabled !== false).map(r => r.key.trim()).filter(Boolean);
  const body = id
    ? JSON.stringify({ channel_id: id })
    : JSON.stringify({
        base_url: document.getElementById('f-url').value.trim(),
        keys,
      });
  const old = btn.textContent;
  btn.disabled = true; btn.textContent = t('fetchingModels');
  const r = await api('/fetch-models', { method: 'POST', body });
  btn.disabled = false; btn.textContent = old;

  if (!r || r.error) { toast(r?.error || t('failed'), 'error'); return; }
  lastFetchedModels = r.models || [];
  // 已选择的模型从文本框读取并回勾
  renderChannelModelPicker();
}

function renderChannelModelPicker() {
  const box = document.getElementById('f-models-picker');
  if (!box) return;
  const q = String(document.getElementById('f-models-search').value || '').trim().toLowerCase();
  const ta = document.getElementById('f-models');
  const set = new Set(ta.value.split('\\n').map(s=>s.trim()).filter(Boolean));
  const list = lastFetchedModels.filter(m => !q || String(m).toLowerCase().includes(q));
  if (lastFetchedModels.length === 0) {
    box.innerHTML = '<div class="model-picker-empty">' + t('modelPickerEmpty') + '</div>';
    return;
  }
  box.innerHTML = list.length ? list.map(m => {
    const checked = set.has(m) ? ' checked' : '';
    return '<label><input type="checkbox" class="model-cb" value="' + esc(m) + '"' + checked + ' onclick="toggleChannelModel(this,\\'' + esc(m) + '\\')">' + esc(m) + '</label>';
  }).join('') : '<div class="model-picker-empty">' + t('modelSearchEmpty') + '</div>';
}

function toggleChannelModel(cb, model) {
  const ta = document.getElementById('f-models');
  const lines = ta.value.split('\\n').map(s=>s.trim()).filter(Boolean);
  const set = new Set(lines);
  if (cb.checked) set.add(model);
  else set.delete(model);
  ta.value = Array.from(set).join('\\n');
}

// ---- 渠道 API 密钥（支持逐条启用 / 禁用） ----
function renderKeyRows() {
  const box = document.getElementById('f-keys-list');
  if (!box) return;
  box.innerHTML = keyRows.map((row, i) =>
    '<div class="key-row" data-i="' + i + '">' +
      '<label class="key-switch"><input type="checkbox" class="key-en" data-i="' + i + '"' + (row.enabled ? ' checked' : '') + ' onchange="toggleKeyRow(this)"><span class="key-slider"></span></label>' +
      '<input class="key-val" value="' + esc(row.key) + '" placeholder="sk-..." oninput="updateKeyRow(' + i + ')">' +
      '<button type="button" class="map-del" onclick="removeKeyRow(' + i + ')">✕</button>' +
    '</div>'
  ).join('');
  box.querySelectorAll('.key-row').forEach(el => {
    const idx = Number(el.dataset.i);
    el.classList.toggle('key-disabled', keyRows[idx] ? keyRows[idx].enabled === false : false);
  });
}

function updateKeyRow(i) {
  const el = document.querySelector('.key-row[data-i="' + i + '"]');
  if (!el) return;
  keyRows[i].key = el.querySelector('.key-val').value.trim();
}

function toggleKeyRow(cb) {
  const i = Number(cb.dataset.i);
  keyRows[i].enabled = cb.checked;
  const el = document.querySelector('.key-row[data-i="' + i + '"]');
  if (el) el.classList.toggle('key-disabled', !cb.checked);
}

function addKeyRow() {
  keyRows.push({ key: '', enabled: true });
  renderKeyRows();
}

function removeKeyRow(i) {
  keyRows.splice(i, 1);
  renderKeyRows();
}

// ---- 公开模型 → 上游模型 映射编辑 ----
// 上游模型下拉（combobox）：点击/输入时拉取上游列表并实时筛选，可点选也可手动输入
let mapUpCtx = { sig: '', list: [] };   // 按渠道缓存的上游模型列表
let mapUpFiltered = [];                  // 当前下拉框筛选项（供点选读取）

// 当前渠道标识（编辑中渠道 id，新增渠道则用 API 主机）
function mapChannelSig() {
  const idEl = document.getElementById('f-ch-id');
  const urlEl = document.getElementById('f-url');
  return (idEl && idEl.value) || (urlEl && urlEl.value.trim()) || '';
}

// 惰性拉取当前渠道的上游模型列表，同渠道只拉一次
async function ensureMapUpstreamModels() {
  const sig = mapChannelSig();
  if (!sig) return [];
  if (mapUpCtx.sig === sig && mapUpCtx.list.length) return mapUpCtx.list;
  const idEl = document.getElementById('f-ch-id');
  const urlEl = document.getElementById('f-url');
  const body = (idEl && idEl.value)
    ? JSON.stringify({ channel_id: idEl.value })
    : JSON.stringify({
        base_url: urlEl ? urlEl.value.trim() : '',
        keys: keyRows.filter(r => r.enabled !== false).map(r => r.key.trim()).filter(Boolean),
      });
  let list = [];
  try {
    const r = await api('/fetch-models', { method: 'POST', body });
    if (r && !r.error && Array.isArray(r.models)) list = r.models;
  } catch (e) { /* 拉取失败时保持空列表 */ }
  mapUpCtx = { sig, list };
  return list;
}

// 元素点击/输入 → 打开下拉（输入时同步筛选）
async function mapUpFocus(el, i) { await openMapUpDropdown(el, i); }
async function mapUpInput(el, i) {
  updateModelMapRow(i);
  await openMapUpDropdown(el, i);
}

async function openMapUpDropdown(el, i) {
  const list = await ensureMapUpstreamModels();
  // 关闭其它行已打开的下拉
  document.querySelectorAll('.map-up-listbox').forEach(n => { if (n.closest('.map-row') !== el.closest('.map-row')) n.remove(); });
  const q = String(el.value || '').trim().toLowerCase();
  mapUpFiltered = list.filter(m => !q || String(m).toLowerCase().includes(q));
  const container = el.closest('.map-up-wrap') || el.closest('.map-row');
  let dl = container.querySelector('.map-up-listbox');
  if (!dl) { dl = document.createElement('div'); dl.className = 'map-up-listbox'; container.appendChild(dl); }
  dl.innerHTML = mapUpFiltered.length
    ? mapUpFiltered.map((m, j) => '<div class="map-up-item" onmousedown="pickMapUpModel(' + i + ',' + j + ')">' + esc(String(m)) + '</div>').join('')
    : '<div class="map-up-empty">' + t('noMatchingModels') + '</div>';
  dl.style.display = 'block';
}

function closeMapUpDropdown() {
  setTimeout(() => { document.querySelectorAll('.map-up-listbox').forEach(n => n.remove()); }, 150);
}

function pickMapUpModel(i, j) {
  const m = mapUpFiltered[j];
  if (m == null) return;
  modelMapRows[i].upstream = String(m);
  const rowEl = document.querySelector('.map-row[data-i="' + i + '"]');
  const up = rowEl && rowEl.querySelector('.map-up');
  if (up) up.value = String(m);
  closeMapUpDropdown();
}

function renderModelMapRows() {
  const box = document.getElementById('f-modelmap');
  if (!box) return;
  if (modelMapRows.length === 0) { box.innerHTML = ''; return; }
  box.innerHTML = modelMapRows.map((row, i) =>
    '<div class="map-row" data-i="' + i + '">' +
      '<input class="map-pub" value="' + esc(row.public) + '" placeholder="' + t('mapPublicModelPh') + '" oninput="updateModelMapRow(' + i + ')">' +
      '<span class="map-arrow">→</span>' +
      '<span class="map-up-wrap">' +
        '<input class="map-up" autocomplete="off" value="' + esc(row.upstream) + '" placeholder="' + t('mapUpstreamPh') + '"' +
          ' onfocus="mapUpFocus(this,' + i + ')" oninput="mapUpInput(this,' + i + ')" onblur="closeMapUpDropdown()">' +
      '</span>' +
      '<button type="button" class="map-del" onclick="removeModelMapRow(' + i + ')">✕</button>' +
    '</div>'
  ).join('');
}

function updateModelMapRow(i) {
  const rowEl = document.querySelector('.map-row[data-i="' + i + '"]');
  if (!rowEl) return;
  modelMapRows[i].public = rowEl.querySelector('.map-pub').value.trim();
  modelMapRows[i].upstream = rowEl.querySelector('.map-up').value.trim();
}

function addModelMapRow(pub, upstream) {
  modelMapRows.push({ public: pub || '', upstream: upstream || '' });
  renderModelMapRows();
}

function removeModelMapRow(i) {
  modelMapRows.splice(i, 1);
  renderModelMapRows();
}

async function saveCh(id) {
  const name = document.getElementById('f-name').value.trim();
  const base_url = document.getElementById('f-url').value.trim();
  const path = document.getElementById('f-path').value.trim();
  const keys = keyRows.map(r => ({ key: r.key.trim(), enabled: r.enabled })).filter(r => r.key);
  const models = document.getElementById('f-models').value.split('\\n').map(s=>s.trim()).filter(Boolean);

  if (!name || !base_url) { toast(t('nameUrlRequired'), 'error'); return; }

  // 收集「公开模型 → 上游模型」映射，忽略公开名为空的行
  const model_map = {};
  for (const row of modelMapRows) {
    const p = (row.public || '').trim();
    if (!p) continue;
    model_map[p] = ((row.upstream || '').trim()) || p;
  }

  const body = JSON.stringify({ name, base_url, path, keys, models, model_map });
  const r = id
    ? await api('/channels/' + id, { method: 'PUT', body })
    : await api('/channels', { method: 'POST', body });

  if (r && !r.error) {
    toast(id ? t('channelUpdated') : t('channelCreated'), 'success');
    closeModal();
    await loadData();
    render();
  } else {
    toast(r?.error || t('saveFailed'), 'error');
  }
}

async function toggleCh(id) {
  const r = await api('/channels/' + id + '/toggle', { method: 'PATCH' });
  if (r && !r.error) { await loadData(); render(); }
}

// ============ Model Routes (只读展示) ============
function channelNameById(id) {
  const ch = channels.find(c => c.id === id);
  return ch ? (ch.name || id) : (id || '-');
}

function renderRoutes() {
  document.getElementById('routes-title').textContent = t('modelRoutes');
  document.getElementById('routes-info').innerHTML = '<p>' + t('routesInfo') + '</p>';

  const head = document.getElementById('routes-thead');
  head.innerHTML = '<th>' + [t('publicModel'), t('routeTargetCol')].join('</th><th>') + '</th>';

  const tb = document.getElementById('routes-tbody');

  // 汇总所有启用且有密钥渠道的模型（model_map 显式映射 + models 同名透传），按首次出现顺序排列
  const rows = [];     // { public, channel, upstream, direct }
  const order = [];    // 公开模型唯一顺序
  const idx = new Map();
  for (const ch of channels) {
    if (ch.enabled === false || !ch.keys || ch.keys.length === 0) continue;
    const mm = (ch.model_map && typeof ch.model_map === 'object') ? ch.model_map : {};
    for (const pub of Object.keys(mm)) {
      const p = String(pub).trim();
      const um = String(mm[pub]).trim();
      if (!p) continue;
      if (!idx.has(p)) { idx.set(p, order.length); order.push(p); }
      rows.push({ public: p, channel: ch, upstream: um || p, direct: false });
    }
    // models 同名透传（不应与 model_map 的公开名重复，仅补充未映射的）
    if (Array.isArray(ch.models)) {
      for (const pub of ch.models) {
        const p = String(pub || '').trim();
        if (!p || rows.some(r => r.channel.id === ch.id && r.public === p)) continue;
        if (!idx.has(p)) { idx.set(p, order.length); order.push(p); }
        rows.push({ public: p, channel: ch, upstream: p, direct: true });
      }
    }
  }

  if (order.length === 0) {
    tb.innerHTML = '<tr><td colspan="2" class="empty">' + t('routesNone') + '</td></tr>';
    return;
  }

  tb.innerHTML = order.map(p => {
    const targets = rows.filter(r => r.public === p);
    const targetHtml = targets.map(r => {
      return '<div style="display:flex;align-items:center;justify-content:space-between;gap:12px;padding:6px 0;border-bottom:1px solid var(--border)">' +
        '<span>' + esc(r.channel.name || r.channel.id) +
          ' <span style="color:var(--text-2);font-size:12px">(' + esc(shortHost(r.channel.base_url)) + ')</span></span>' +
        '<span><code style="background:var(--bg-0);padding:2px 8px;border-radius:4px;font-size:12px;color:var(--primary)">' + esc(r.upstream) + '</code></span>' +
      '</div>';
    }).join('');

    return '<tr>' +
      '<td style="vertical-align:top;white-space:nowrap">' +
        '<code style="background:var(--bg-0);padding:3px 8px;border-radius:4px;font-size:13px">' + esc(p) + '</code>' +
        (targets.some(r => r.direct) ? ' <span class="tag-direct">' + t('direct') + '</span>' : '') +
      '</td>' +
      '<td style="padding-top:4px;padding-bottom:4px">' + targetHtml + '</td>' +
    '</tr>';
  }).join('');
}

// ============ API Keys ============
function renderApiKeys() {
  const tb = document.getElementById('ak-tbody');
  if (!apiKeys.length) {
    tb.innerHTML = '<tr><td colspan="6" class="empty">' + t('noApiKeys') + '</td></tr>';
    return;
  }
  tb.innerHTML = apiKeys.map(k => {
    const chIds = k.channel_ids || [];
    const chNames = chIds.length > 0
      ? chIds.map(id => { const ch = channels.find(c => c.id === id); return ch ? esc(ch.name) : '?'; }).join(', ')
      : '<span style="color:var(--text-2)">' + t('allChannels') + '</span>';

    return \`
    <tr>
      <td>\${esc(k.name)}</td>
      <td><span class="key-mono">\${maskKey(k.key)}</span>
        <button class="btn btn-sm btn-ghost" style="margin-left:8px" data-key="\${esc(k.key)}" onclick="copyKey(this)">\${t('copy')}</button>
      </td>
      <td>\${chNames}</td>
      <td>\${fmtDate(k.created_at)}</td>
      <td><span class="badge \${k.enabled?'badge-on':'badge-off'}">\${k.enabled?t('on'):t('off')}</span></td>
      <td style="white-space:nowrap">
        <button class="btn btn-sm btn-ghost" onclick="showEditAkModal('\${k.id}')">\${t('edit')}</button>
        <button class="btn btn-sm btn-ghost" onclick="toggleAk('\${k.id}')">\${k.enabled?t('disable'):t('enable')}</button>
        <button class="btn btn-sm btn-danger" onclick="confirmDel('apikey','\${k.id}','\${esc(k.name)}')">\${t('delete')}</button>
      </td>
    </tr>
  \`}).join('');
}

function channelCheckboxes(selectedIds) {
  if (!channels.length) return '<div class="form-help">' + t('noChannels') + '</div>';
  return channels.map(ch => {
    const checked = selectedIds.includes(ch.id) ? 'checked' : '';
    return '<label style="display:flex;align-items:center;gap:8px;cursor:pointer;margin-bottom:6px;font-size:14px">' +
      '<input type="checkbox" class="ch-bind-cb" value="' + ch.id + '" ' + checked + ' style="width:auto">' +
      '<span>' + esc(ch.name) + '</span>' +
      '<span style="color:var(--text-2);font-size:12px;margin-left:auto">' + esc(ch.base_url) + '</span>' +
    '</label>';
  }).join('');
}

function getSelectedChannelIds() {
  return Array.from(document.querySelectorAll('.ch-bind-cb:checked')).map(cb => cb.value);
}

function showAkModal() {
  openModal(\`
    <h3>\${t('genKeyTitle')}</h3>
    <div class="form-group">
      <label>\${t('nameOptional')}</label>
      <input id="f-akname" placeholder="\${t('nameOptPlaceholder')}">
    </div>
    <div class="form-group">
      <label>\${t('selectChannels')}</label>
      <div style="background:var(--bg-0);border:1px solid var(--border);border-radius:6px;padding:12px;max-height:200px;overflow-y:auto">
        \${channelCheckboxes([])}
      </div>
      <div class="form-help">\${t('channelBindHelp')}</div>
    </div>
    <div class="modal-actions">
      <button class="btn btn-ghost" onclick="closeModal()">\${t('cancel')}</button>
      <button class="btn btn-primary" onclick="genAk()">\${t('generate')}</button>
    </div>
  \`);
}

function showEditAkModal(id) {
  const k = apiKeys.find(x => x.id === id);
  if (!k) return;
  openModal(\`
    <h3>\${t('editKey')}</h3>
    <div class="form-group">
      <label>\${t('name')}</label>
      <input id="f-akname-edit" value="\${esc(k.name)}">
    </div>
    <div class="form-group">
      <label>\${t('selectChannels')}</label>
      <div style="background:var(--bg-0);border:1px solid var(--border);border-radius:6px;padding:12px;max-height:200px;overflow-y:auto">
        \${channelCheckboxes(k.channel_ids || [])}
      </div>
      <div class="form-help">\${t('channelBindHelp')}</div>
    </div>
    <div class="modal-actions">
      <button class="btn btn-ghost" onclick="closeModal()">\${t('cancel')}</button>
      <button class="btn btn-primary" onclick="saveEditAk('\${id}')">\${t('save')}</button>
    </div>
  \`);
}

async function genAk() {
  const name = document.getElementById('f-akname').value.trim() || 'Unnamed';
  const channel_ids = getSelectedChannelIds();
  const r = await api('/apikeys', { method: 'POST', body: JSON.stringify({ name, channel_ids }) });
  if (r && !r.error) {
    closeModal();
    openModal(\`
      <h3>\${t('keyCreated')}</h3>
      <p style="color:var(--text-1);margin-bottom:16px">\${t('keyCreatedHint')}</p>
      <div class="form-group">
        <input type="text" value="\${r.key}" readonly onclick="this.select()" style="font-family:monospace;font-size:13px">
      </div>
      <div class="modal-actions">
        <button class="btn btn-primary" onclick="copyText('\${r.key}');closeModal()">\${t('copyClose')}</button>
      </div>
    \`);
    await loadData();
    render();
  } else {
    toast(r?.error || t('failed'), 'error');
  }
}

async function toggleAk(id) {
  const k = apiKeys.find(x => x.id === id);
  if (!k) return;
  const r = await api('/apikeys/' + id, { method: 'PATCH', body: JSON.stringify({ enabled: !k.enabled }) });
  if (r && !r.error) { await loadData(); render(); }
}

async function saveEditAk(id) {
  const name = document.getElementById('f-akname-edit').value.trim();
  const channel_ids = getSelectedChannelIds();
  const r = await api('/apikeys/' + id, { method: 'PATCH', body: JSON.stringify({ name, channel_ids }) });
  if (r && !r.error) {
    toast(t('channelUpdated'), 'success');
    closeModal();
    await loadData();
    render();
  } else {
    toast(r?.error || t('failed'), 'error');
  }
}

// ============ Error Logs ============
function renderErrorHeaders() {
  document.getElementById('error-title').textContent = t('errorLogs');
  document.getElementById('error-refresh-btn').textContent = t('refreshError');
  document.getElementById('error-date-label').textContent = t('errorDate');
  const dateInput = document.getElementById('error-date');
  if (!dateInput.value) dateInput.value = todayBeijing();
}

async function loadErrors() {
  const date = document.getElementById('error-date').value || todayBeijing();
  const errData = await api('/errors?date=' + date);
  renderErrors(errData);
}

function renderErrors(errData) {
  const container = document.getElementById('error-container');
  if (!errData || !errData.channels || errData.channels.length === 0) {
    container.innerHTML = '<div class="empty">' + t('noErrors') + '</div>';
    return;
  }
  container.innerHTML = errData.channels.map(ch => {
    const errors = (ch.errors || []).slice().reverse();
    const rows = errors.map(e => {
      const time = e.time ? new Date(e.time).toLocaleString() : '-';
      const statusBadge = e.status >= 500
        ? '<span class="badge badge-off">' + e.status + '</span>'
        : e.status === 404
          ? '<span class="badge" style="background:rgba(245,158,11,.12);color:var(--warning)">' + e.status + '</span>'
          : e.status > 0
            ? '<span class="badge" style="background:rgba(99,102,241,.12);color:var(--primary)">' + e.status + '</span>'
            : '<span class="badge badge-off">ERR</span>';
      const modelCell = '<div style="font-family:monospace;font-size:13px">' + esc(e.model || '-') + '</div>' +
        '<div style="font-family:monospace;font-size:11px;color:var(--text-2);margin-top:2px">\u2192 ' + esc(e.upstream_model || '-') +
        (e.base_url ? ' @ ' + esc(shortHost(e.base_url)) : '') + '</div>';
      return '<tr>' +
        '<td style="white-space:nowrap;font-size:13px;color:var(--text-2)">' + time + '</td>' +
        '<td>' + modelCell + '</td>' +
        '<td>' + statusBadge + '</td>' +
        '<td title="' + esc(e.message) + '">' +
        '<div class="err-wrap">' +
          '<span class="err-caret" title="' + t('expandMsg') + '" onclick="toggleErrMsg(this)">▶</span>' +
          '<span class="err-text">' + esc(e.message) + '</span>' +
          '<button type="button" class="err-copy" title="' + t('copyMsg') + '" onclick="copyErrMsg(this)">⧉</button>' +
        '</div>' +
      '</td>' +
      '</tr>';
    }).join('');

    return '<div style="background:var(--bg-2);border:1px solid var(--border);border-radius:var(--radius);padding:24px;margin-bottom:16px">' +
      '<h4 style="display:flex;align-items:center;gap:8px">' +
        '<span style="width:8px;height:8px;border-radius:50%;background:var(--danger);display:inline-block"></span> ' +
        esc(ch.channel_name) +
        ' <span style="color:var(--text-2);font-size:13px;font-weight:400">' + errors.length + ' ' + t('errorsToday') + '</span>' +
      '</h4>' +
      '<div class="table-container" style="margin-top:12px">' +
        '<table><thead><tr>' +
          '<th>' + t('errorTime') + '</th>' +
          '<th>' + t('errorModel') + '</th>' +
          '<th>' + t('errorStatus') + '</th>' +
          '<th>' + t('errorMessage') + '</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table>' +
      '</div>' +
    '</div>';
  }).join('');
}

// 错误日志：展开/收起完整报错信息
function toggleErrMsg(caret) {
  caret.parentElement.classList.toggle('open');
}

// 错误日志：一键复制完整报错信息
async function copyErrMsg(btn) {
  const textEl = btn.parentElement.querySelector('.err-text');
  const msg = textEl ? textEl.textContent : '';
  if (!msg) { toast(t('copyFail'), 'error'); return; }
  try {
    await navigator.clipboard.writeText(msg);
    toast(t('copied'), 'success');
  } catch {
    // 剪贴板 API 不可用时回退到 textarea 复制
    const ta = document.createElement('textarea');
    ta.value = msg;
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); toast(t('copied'), 'success'); } catch { toast(t('copyFail'), 'error'); }
    document.body.removeChild(ta);
  }
}

// ============ Shared ============
function confirmDel(type, id, name) {
  openModal(\`
    <h3>\${t('confirmDelete')}</h3>
    <p style="color:var(--text-1);margin-bottom:24px">\${t('confirmDeleteMsg').replace('{name}', name)}</p>
    <div class="modal-actions">
      <button class="btn btn-ghost" onclick="closeModal()">\${t('cancel')}</button>
      <button class="btn btn-danger" id="del-btn">\${t('delete')}</button>
    </div>
  \`);
  document.getElementById('del-btn').onclick = async () => {
    closeModal();
    const paths = { channel: '/channels/', apikey: '/apikeys/', route: '/routes/' };
    const r = await api((paths[type] || '/routing/') + id, { method: 'DELETE' });
    if (r && !r.error) {
      toast(t('deleted'), 'success');
      await loadData();
      render();
    }
  };
}

// ============ Modal ============
function openModal(html) {
  document.getElementById('modal-box').innerHTML = html;
  document.getElementById('modal-overlay').style.display = 'flex';
}
function closeModal() {
  document.getElementById('modal-overlay').style.display = 'none';
}

// ============ Toast ============
function toast(msg, type) {
  const c = document.getElementById('toast-container');
  const el = document.createElement('div');
  el.className = 'toast ' + (type || '');
  el.textContent = msg;
  c.appendChild(el);
  setTimeout(() => el.remove(), 3500);
}

// ============ Util ============
function esc(s) {
  if (!s) return '';
  const d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}
function maskKey(k) { return k && k.length > 12 ? k.slice(0,7) + '...' + k.slice(-4) : k; }
function shortHost(url) {
  if (!url) return '';
  try { const u = new URL(url); return (u.hostname || url).replace(/^www\./, ''); } catch { return url; }
}
function todayBeijing() {
  return new Date(new Date().getTime() + 8 * 60 * 60 * 1000).toISOString().slice(0, 10);
}
function fmtDate(d) { return d ? new Date(d).toLocaleDateString() : '-'; }
function copyKey(btn) { copyText(btn.dataset.key); }
async function copyText(txt) {
  try { await navigator.clipboard.writeText(txt); toast(t('copied'), 'success'); }
  catch { toast(t('copyFailed'), 'error'); }
}

// ============ Events ============
document.getElementById('modal-overlay').addEventListener('click', e => { if (e.target === e.currentTarget) closeModal(); });
document.getElementById('login-pwd').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') closeModal(); });

// ============ Init ============
(async () => {
  if (token) {
    showMain();
    await loadData();
    render();
  } else {
    showLogin();
  }
})();
</script>
</body>
</html>`;
}
