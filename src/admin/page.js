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

/* Layout */
.main-view{display:flex;min-height:100vh}
.sidebar{width:260px;background:var(--bg-1);border-right:1px solid var(--border);display:flex;flex-direction:column;position:fixed;height:100vh;z-index:10}
.sidebar-header{padding:24px 20px;border-bottom:1px solid var(--border)}
.sidebar-header .logo{font-size:20px;font-weight:700;background:linear-gradient(135deg,#6366f1,#a78bfa);-webkit-background-clip:text;-webkit-text-fill-color:transparent}
.sidebar-nav{flex:1;padding:12px}
.nav-item{display:block;padding:10px 16px;color:var(--text-1);border-radius:var(--radius);margin-bottom:4px;cursor:pointer;transition:all .15s;font-size:14px;font-weight:500}
.nav-item:hover{background:var(--bg-hover);color:var(--text-0)}
.nav-item.active{background:var(--primary);color:#fff}
.sidebar-footer{padding:16px;border-top:1px solid var(--border)}
.content{flex:1;margin-left:260px;padding:32px;max-width:1200px}

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

/* Badge */
.badge{display:inline-block;padding:3px 10px;border-radius:9999px;font-size:12px;font-weight:500}
.badge-on{background:rgba(34,197,94,.12);color:var(--success)}
.badge-off{background:rgba(239,68,68,.12);color:var(--danger)}

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

/* Language toggle */
.lang-toggle{display:flex;background:var(--bg-0);border:1px solid var(--border);border-radius:6px;overflow:hidden;margin-bottom:8px}
.lang-btn{flex:1;padding:6px 0;text-align:center;font-size:12px;font-weight:500;color:var(--text-2);cursor:pointer;transition:all .15s;border:none;background:transparent}
.lang-btn:hover{color:var(--text-0)}
.lang-btn.active{background:var(--primary);color:#fff}

/* Usage monitor */
.usage-card{background:var(--bg-2);border:1px solid var(--border);border-radius:var(--radius);padding:24px;margin-bottom:16px}
.usage-card h4{font-size:16px;font-weight:600;margin-bottom:16px;display:flex;align-items:center;gap:8px}
.usage-row{margin-bottom:14px}
.usage-row:last-child{margin-bottom:0}
.usage-label{display:flex;justify-content:space-between;margin-bottom:5px;font-size:13px;color:var(--text-1)}
.usage-label .usage-val{font-weight:600;color:var(--text-0)}
.progress-bar{height:8px;background:var(--bg-0);border-radius:4px;overflow:hidden}
.progress-fill{height:100%;border-radius:4px;transition:width .4s ease}
.progress-ok{background:linear-gradient(90deg,#22c55e,#4ade80)}
.progress-warn{background:linear-gradient(90deg,#f59e0b,#fbbf24)}
.progress-danger{background:linear-gradient(90deg,#ef4444,#f87171)}
.date-picker{display:flex;gap:8px;align-items:center;margin-bottom:20px}
.date-picker input[type="date"]{width:180px}

/* 分页控件 */
.pagination{display:flex;align-items:center;justify-content:center;gap:4px;margin-top:14px;padding-top:14px;border-top:1px solid var(--border)}
.pagination button{min-width:32px;height:28px;border:1px solid var(--border);border-radius:4px;background:var(--bg-1);color:var(--text-1);font-size:12px;cursor:pointer;transition:all .15s;padding:0 8px}
.pagination button:hover:not(:disabled):not(.pg-active){background:var(--bg-3);border-color:var(--primary)}
.pagination button:disabled{opacity:.35;cursor:default}
.pagination .pg-active{background:var(--primary);color:#fff;border-color:var(--primary);font-weight:600}
.pagination .pg-info{font-size:12px;color:var(--text-2);margin:0 4px}
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
    <div style="margin-top:16px">
      <div class="lang-toggle" id="login-lang-toggle">
        <button class="lang-btn" onclick="setLang('en')">English</button>
        <button class="lang-btn" onclick="setLang('zh')">中文</button>
      </div>
    </div>
  </div>
</div>

<!-- ====== Main ====== -->
<div id="main-view" class="main-view" style="display:none">
  <aside class="sidebar">
    <div class="sidebar-header"><div class="logo">AI Gateway</div></div>
    <nav class="sidebar-nav" id="sidebar-nav">
      <a class="nav-item active" data-section="dashboard" onclick="navigate('dashboard')"></a>
      <a class="nav-item" data-section="channels" onclick="navigate('channels')"></a>
      <a class="nav-item" data-section="routes" onclick="navigate('routes')"></a>
      <a class="nav-item" data-section="usage" onclick="navigate('usage')"></a>
      <a class="nav-item" data-section="apikeys" onclick="navigate('apikeys')"></a>
    </nav>
    <div class="sidebar-footer">
      <div class="lang-toggle" id="sidebar-lang-toggle" style="margin-bottom:8px">
        <button class="lang-btn" onclick="setLang('en')">EN</button>
        <button class="lang-btn" onclick="setLang('zh')">中文</button>
      </div>
      <button class="btn btn-ghost btn-full" onclick="doLogout()" id="logout-btn"></button>
    </div>
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

    <!-- Model Routes -->
    <section id="section-routes" class="section" style="display:none">
      <div class="section-header">
        <h2 id="rt-title"></h2>
        <button class="btn btn-primary" onclick="showRouteModal()" id="rt-add-btn"></button>
      </div>
      <div class="table-container">
        <table>
          <thead><tr id="rt-thead"></tr></thead>
          <tbody id="rt-tbody"></tbody>
        </table>
      </div>
    </section>

    <!-- Usage Monitor -->
    <section id="section-usage" class="section" style="display:none">
      <div class="section-header">
        <h2 id="usage-title"></h2>
        <button class="btn btn-ghost" onclick="loadUsage()" id="usage-refresh-btn"></button>
      </div>
      <div class="date-picker">
        <label id="usage-date-label" style="margin:0;white-space:nowrap"></label>
        <input type="date" id="usage-date" onchange="loadUsage()">
      </div>
      <div id="usage-container"></div>
      <div style="margin-top:32px">
        <div class="section-header"><h2 id="error-title"></h2></div>
        <div id="error-container"></div>
      </div>
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
    baseUrl: 'Base URL',
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
    urlHelp: 'Include the version path, e.g. /v1',
    keysLabel: 'API Keys (one per line)',
    keysPlaceholder: 'sk-xxx\\nsk-yyy',
    modelsLabel: 'Models (one per line, empty = accept all)',
    modelsPlaceholder: 'gpt-4o\\nclaude-3-opus',
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
    usageMonitor: 'Usage Monitor',
    totalUsage: 'Daily Total',
    perModelUsage: 'Per Model',
    noQuotaChannels: 'No usage data for the selected date.',
    remaining: 'remaining',
    unlimited: 'Unlimited',
    usageDate: 'Date',
    refreshUsage: 'Refresh',
    noModelUsageYet: 'No requests yet',
    requests: 'Requests',
    tokens: 'Tokens',
    estimatedCost: 'Cost',
    promptTokens: 'Input',
    completionTokens: 'Output',
    usage: 'Usage',
    noUsageYet: 'No usage yet',
    boundChannels: 'Bound Channels',
    allChannels: 'All Channels',
    selectChannels: 'Select Channels',
    channelBindHelp: 'Only route to selected channels. Empty = all channels.',
    editKey: 'Edit Key',
    errorLogs: 'Error Logs',
    noErrors: 'No errors today.',
    errorTime: 'Time',
    errorModel: 'Model',
    errorStatus: 'Status',
    errorMessage: 'Message',
    errorKey: 'Key',
    errorsToday: 'errors today',
    keysTotal: 'keys',
    cooldown: 'Cooldown',
    modelRoutes: 'Model Routes',
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
    baseUrl: '基础 URL',
    keys: '密钥数',
    models: '模型',
    priority: '优先级',
    weight: '权重',
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
    urlHelp: '需包含版本路径，例如 /v1',
    keysLabel: 'API 密钥（每行一个）',
    keysPlaceholder: 'sk-xxx\\nsk-yyy',
    modelsLabel: '模型列表（每行一个，留空表示接受所有模型）',
    modelsPlaceholder: 'gpt-4o\\nclaude-3-opus',
    modelsHelp: '仅匹配这些模型的请求会路由到此渠道。留空则接受任何模型。',
    priorityHelp: '数值越小优先级越高，优先尝试。',
    weightHelp: '同优先级组内的相对权重。',
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
    usageMonitor: '用量监控',
    totalUsage: '每日总量',
    perModelUsage: '单模型用量',
    noQuotaChannels: '所选日期暂无用量数据。',
    remaining: '剩余',
    unlimited: '不限制',
    usageDate: '日期',
    refreshUsage: '刷新',
    noModelUsageYet: '暂无请求记录',
    requests: '请求',
    tokens: 'Tokens',
    estimatedCost: '费用',
    promptTokens: '输入',
    completionTokens: '输出',
    cachedTokens: '缓存',
    usage: '用量',
    noUsageYet: '暂无用量',
    boundChannels: '绑定渠道',
    allChannels: '全部渠道',
    selectChannels: '选择渠道',
    channelBindHelp: '仅路由到选中的渠道。不选则使用全部渠道。',
    editKey: '编辑密钥',
    errorLogs: '错误日志',
    noErrors: '今日暂无错误。',
    errorTime: '时间',
    errorModel: '模型',
    errorStatus: '状态码',
    errorMessage: '错误信息',
    errorKey: '密钥',
    errorsToday: '个错误',
    keysTotal: '个密钥',
    cooldown: '冷却倒计时',
    modelRoutes: '模型路由',
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
    routeModelRequired: '公开模型名和目标渠道不能为空',
    selectChannel: '选择一个渠道',
  },
};

let lang = localStorage.getItem('ag_lang') || (navigator.language.startsWith('zh') ? 'zh' : 'en');
function t(key) { return I18N[lang]?.[key] || I18N.en[key] || key; }
function setLang(l) { lang = l; localStorage.setItem('ag_lang', l); renderAll(); }
function renderAll() { renderLogin(); renderSidebar(); render(); }

// ============ State ============
let token = localStorage.getItem('ag_token');
let channels = [];
let apiKeys = [];
let routes = [];
let curSection = 'dashboard';

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

let apiKeyUsage = {};

async function loadData() {
  const [ch, ak, aku, rt] = await Promise.all([
    api('/channels'),
    api('/apikeys'),
    api('/apikeys/usage'),
    api('/routes'),
  ]);
  channels = ch || [];
  apiKeys = ak || [];
  apiKeyUsage = (aku && aku.keys) || {};
  routes = rt || [];
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
  document.querySelectorAll('#login-lang-toggle .lang-btn').forEach(b => {
    b.classList.toggle('active', (b.textContent === 'English' && lang === 'en') || (b.textContent === '中文' && lang === 'zh'));
  });
}

function renderSidebar() {
  const navMap = { dashboard: 'dashboard', channels: 'channels', routes: 'modelRoutes', usage: 'usageMonitor', apikeys: 'apiKeys' };
  document.querySelectorAll('#sidebar-nav .nav-item').forEach(el => {
    el.textContent = t(navMap[el.dataset.section]);
    el.classList.toggle('active', el.dataset.section === curSection);
  });
  document.getElementById('logout-btn').textContent = t('signOut');
  document.querySelectorAll('#sidebar-lang-toggle .lang-btn').forEach(b => {
    b.classList.toggle('active', (b.textContent === 'EN' && lang === 'en') || (b.textContent === '中文' && lang === 'zh'));
  });
}

function navigate(section) {
  if (section !== 'usage') syncUsageCooldownTicker(false);
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
  renderRouteHeaders();
  renderApiKeyHeaders();
  if (curSection === 'channels') renderChannels();
  if (curSection === 'routes') renderRoutes();
  if (curSection === 'usage') { renderUsageHeaders(); loadUsage(); }
  if (curSection === 'apikeys') renderApiKeys();
}

function renderRouteHeaders() {
  const tb = document.getElementById('rt-title');
  if (!tb) return;
  document.getElementById('rt-title').textContent = t('modelRoutes');
  document.getElementById('rt-add-btn').textContent = t('addRoute');
  document.getElementById('rt-thead').innerHTML = '<th>'+[t('routeName'),t('publicModel'),t('targetChannel'),t('upstreamModel'),t('priority'),t('weight'),t('status'),t('actions')].join('</th><th>')+'</th>';
}

function renderChannelHeaders() {
  document.getElementById('ch-title').textContent = t('channels');
  document.getElementById('ch-add-btn').textContent = t('addChannel');
  document.getElementById('ch-thead').innerHTML = '<th>'+[t('name'),t('baseUrl'),t('keys'),t('models'),t('priority'),t('weight'),t('status'),t('actions')].join('</th><th>')+'</th>';
}

function renderApiKeyHeaders() {
  document.getElementById('ak-title').textContent = t('apiKeys');
  document.getElementById('ak-gen-btn').textContent = t('generateKey');
  document.getElementById('ak-thead').innerHTML = '<th>'+[t('name'),t('key'),t('boundChannels'),t('usage'),t('created'),t('status'),t('actions')].join('</th><th>')+'</th>';
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
    tb.innerHTML = '<tr><td colspan="8" class="empty">' + t('noChannels') + '</td></tr>';
    return;
  }
  tb.innerHTML = channels.map(c => \`
    <tr>
      <td><strong>\${esc(c.name)}</strong></td>
      <td class="cell-truncate" title="\${esc(c.base_url)}">\${esc(c.base_url)}</td>
      <td>\${c.keys?.length || 0}</td>
      <td>\${c.models?.length || '<span style="color:var(--text-2)">' + t('all') + '</span>'}</td>
      <td>\${c.priority}</td>
      <td>\${c.weight}</td>
      <td><span class="badge \${c.enabled ? 'badge-on' : 'badge-off'}">\${c.enabled ? t('on') : t('off')}</span></td>
      <td style="white-space:nowrap">
        <button class="btn btn-sm btn-ghost" onclick="showChModal('\${c.id}')">\${t('edit')}</button>
        <button class="btn btn-sm btn-ghost" onclick="toggleCh('\${c.id}')">\${c.enabled ? t('disable') : t('enable')}</button>
        <button class="btn btn-sm btn-danger" onclick="confirmDel('channel','\${c.id}','\${esc(c.name)}')">\${t('delete')}</button>
      </td>
    </tr>
  \`).join('');
}

function showChModal(id) {
  const ch = id ? channels.find(c => c.id === id) : null;
  const title = ch ? t('editChannel') : t('addChannel');
  const html = \`
    <h3>\${title}</h3>
    <div class="form-group">
      <label>\${t('name')}</label>
      <input id="f-name" value="\${ch ? esc(ch.name) : ''}" placeholder="\${t('namePlaceholder')}">
    </div>
    <div class="form-group">
      <label>\${t('baseUrl')}</label>
      <input id="f-url" value="\${ch ? esc(ch.base_url) : ''}" placeholder="\${t('urlPlaceholder')}">
      <div class="form-help">\${t('urlHelp')}</div>
    </div>
    <div class="form-group">
      <label>\${t('keysLabel')}</label>
      <textarea id="f-keys" placeholder="\${t('keysPlaceholder')}">\${ch ? (ch.keys||[]).join('\\n') : ''}</textarea>
    </div>
    <div class="form-group">
      <label>\${t('modelsLabel')}</label>
      <textarea id="f-models" style="min-height:80px" placeholder="\${t('modelsPlaceholder')}">\${ch ? (ch.models||[]).join('\\n') : ''}</textarea>
      <div class="form-help">\${t('modelsHelp')}</div>
    </div>
    <div class="form-row">
      <div class="form-group">
        <label>\${t('priority')}</label>
        <input type="number" id="f-pri" value="\${ch ? ch.priority : 0}" min="0">
        <div class="form-help">\${t('priorityHelp')}</div>
      </div>
      <div class="form-group">
        <label>\${t('weight')}</label>
        <input type="number" id="f-wt" value="\${ch ? ch.weight : 10}" min="1">
        <div class="form-help">\${t('weightHelp')}</div>
      </div>
    </div>
    <div class="modal-actions">
      <button class="btn btn-ghost" onclick="closeModal()">\${t('cancel')}</button>
      <button class="btn btn-primary" onclick="saveCh('\${id||''}')">\${t('save')}</button>
    </div>
  \`;
  openModal(html);
}

async function saveCh(id) {
  const name = document.getElementById('f-name').value.trim();
  const base_url = document.getElementById('f-url').value.trim();
  const keys = document.getElementById('f-keys').value.split('\\n').map(s=>s.trim()).filter(Boolean);
  const models = document.getElementById('f-models').value.split('\\n').map(s=>s.trim()).filter(Boolean);
  const priority = parseInt(document.getElementById('f-pri').value) || 0;
  const weight = parseInt(document.getElementById('f-wt').value) || 1;

  if (!name || !base_url) { toast(t('nameUrlRequired'), 'error'); return; }

  const body = JSON.stringify({ name, base_url, keys, models, priority, weight });
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

// ============ Model Routes ============
function channelNameById(id) {
  const ch = channels.find(c => c.id === id);
  return ch ? (ch.name || id) : (id || '-');
}

function renderRoutes() {
  const tb = document.getElementById('rt-tbody');
  if (!routes.length) {
    tb.innerHTML = '<tr><td colspan="8" class="empty">' + t('noRoutes') + '</td></tr>';
    return;
  }
  tb.innerHTML = routes.map(r => \`
    <tr>
      <td><strong>\${esc(r.name)}</strong></td>
      <td style="font-family:monospace;font-size:13px">\${esc(r.model)}</td>
      <td>\${esc(channelNameById(r.channel_id))}</td>
      <td style="font-family:monospace;font-size:13px">\${esc(r.upstream_model || '-')}</td>
      <td>\${r.priority}</td>
      <td>\${r.weight}</td>
      <td><span class="badge \${r.enabled ? 'badge-on' : 'badge-off'}">\${r.enabled ? t('on') : t('off')}</span></td>
      <td style="white-space:nowrap">
        <button class="btn btn-sm btn-ghost" onclick="showRouteModal('\${r.id}')">\${t('edit')}</button>
        <button class="btn btn-sm btn-ghost" onclick="toggleRoute('\${r.id}')">\${r.enabled ? t('disable') : t('enable')}</button>
        <button class="btn btn-sm btn-danger" onclick="confirmDel('route','\${r.id}','\${esc(r.model)}')">\${t('delete')}</button>
      </td>
    </tr>
  \`).join('');
}

function routeChannelSelect(selectedId) {
  if (!channels.length) return '<div class="form-help">' + t('noChannels') + '</div>';
  return '<select id="f-rt-channel">' +
    '<option value="">' + t('selectChannel') + '</option>' +
    channels.map(ch =>
      '<option value="' + ch.id + '"' + (ch.id === selectedId ? ' selected' : '') + '>' + esc(ch.name) + '</option>'
    ).join('') +
  '</select>';
}

function showRouteModal(id) {
  const r = id ? routes.find(x => x.id === id) : null;
  const title = r ? t('editRoute') : t('addRoute');
  const html = \`
    <h3>\${title}</h3>
    <div class="form-group">
      <label>\${t('routeName')}</label>
      <input id="f-rt-name" value="\${r ? esc(r.name) : ''}" placeholder="\${r ? '' : esc(t('publicModel'))}">
    </div>
    <div class="form-group">
      <label>\${t('publicModel')}</label>
      <input id="f-rt-model" value="\${r ? esc(r.model) : ''}" placeholder="my-model">
      <div class="form-help">\${t('publicModelHelp')}</div>
    </div>
    <div class="form-group">
      <label>\${t('targetChannel')}</label>
      \${routeChannelSelect(r ? r.channel_id : '')}
    </div>
    <div class="form-group">
      <label>\${t('upstreamModel')}</label>
      <input id="f-rt-upstream" value="\${r ? esc(r.upstream_model || '') : ''}">
      <div class="form-help">\${t('upstreamModelHelp')}</div>
    </div>
    <div class="form-row">
      <div class="form-group">
        <label>\${t('priority')}</label>
        <input type="number" id="f-rt-pri" value="\${r ? r.priority : 0}" min="0">
        <div class="form-help">\${t('priorityHelp')}</div>
      </div>
      <div class="form-group">
        <label>\${t('weight')}</label>
        <input type="number" id="f-rt-wt" value="\${r ? r.weight : 10}" min="1">
        <div class="form-help">\${t('weightHelp')}</div>
      </div>
    </div>
    <div class="modal-actions">
      <button class="btn btn-ghost" onclick="closeModal()">\${t('cancel')}</button>
      <button class="btn btn-primary" onclick="saveRoute('\${id || ''}')">\${t('save')}</button>
    </div>
  \`;
  openModal(html);
}

async function saveRoute(id) {
  const model = document.getElementById('f-rt-model').value.trim();
  const channel_id = document.getElementById('f-rt-channel').value;
  if (!model || !channel_id) { toast(t('routeModelRequired'), 'error'); return; }

  const name = document.getElementById('f-rt-name').value.trim() || model;
  const upstream_model = document.getElementById('f-rt-upstream').value.trim();
  const priority = parseInt(document.getElementById('f-rt-pri').value) || 0;
  const weight = parseInt(document.getElementById('f-rt-wt').value) || 1;

  const body = JSON.stringify({ name, model, channel_id, upstream_model, priority, weight });
  const r = id
    ? await api('/routes/' + id, { method: 'PUT', body })
    : await api('/routes', { method: 'POST', body });

  if (r && !r.error) {
    toast(id ? t('routeUpdated') : t('routeCreated'), 'success');
    closeModal();
    await loadData();
    render();
  } else {
    toast(r?.error || t('saveFailed'), 'error');
  }
}

async function toggleRoute(id) {
  const r = routes.find(x => x.id === id);
  if (!r) return;
  const res = await api('/routes/' + id, { method: 'PATCH', body: JSON.stringify({ enabled: !r.enabled }) });
  if (res && !res.error) { await loadData(); render(); }
}

// ============ API Keys ============
function renderApiKeys() {
  const tb = document.getElementById('ak-tbody');
  if (!apiKeys.length) {
    tb.innerHTML = '<tr><td colspan="7" class="empty">' + t('noApiKeys') + '</td></tr>';
    return;
  }
  tb.innerHTML = apiKeys.map(k => {
    const chIds = k.channel_ids || [];
    const chNames = chIds.length > 0
      ? chIds.map(id => { const ch = channels.find(c => c.id === id); return ch ? esc(ch.name) : '?'; }).join(', ')
      : '<span style="color:var(--text-2)">' + t('allChannels') + '</span>';

    // API 密钥用量统计
    const u = apiKeyUsage[k.id];
    let usageHtml;
    if (u && u.requests > 0) {
      const totalTokens = (u.prompt_tokens || 0) + (u.completion_tokens || 0);
      const cost = calcKeyTotalCost(u);
      usageHtml = '<div style="font-size:12px;line-height:1.6">' +
        '<span style="color:var(--text-0)">' + fmtNum(u.requests) + '</span> <span style="color:var(--text-2)">' + t('requests') + '</span>' +
        ' <span style="color:var(--border);margin:0 4px">·</span> ' +
        '<span style="color:var(--text-0)">' + fmtNum(totalTokens) + '</span> <span style="color:var(--text-2)">' + t('tokens') + '</span>' +
        (totalTokens > 0 ? ' <span style="font-size:11px;color:var(--text-2)">(' + fmtNum(u.prompt_tokens||0) + '↑ ' + fmtNum(u.completion_tokens||0) + '↓)</span>' : '') +
        ' <span style="color:var(--border);margin:0 4px">·</span> ' +
        '<span style="color:var(--success);font-weight:500">' + fmtCost(cost) + '</span>' +
      '</div>';
    } else {
      usageHtml = '<span style="font-size:12px;color:var(--text-2)">' + t('noUsageYet') + '</span>';
    }

    return \`
    <tr>
      <td>\${esc(k.name)}</td>
      <td><span class="key-mono">\${maskKey(k.key)}</span>
        <button class="btn btn-sm btn-ghost" style="margin-left:8px" data-key="\${esc(k.key)}" onclick="copyKey(this)">\${t('copy')}</button>
      </td>
      <td>\${chNames}</td>
      <td>\${usageHtml}</td>
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

// ============ Usage Monitor ============
let usageData = null;
const USAGE_PAGE_SIZE = 10;
const usageKeyPages = {};  // { channelId: currentPage(从1开始) }
let usageCooldownTicker = null;

function formatCooldownLeft(untilMs) {
  const sec = Math.max(0, Math.ceil((untilMs - Date.now()) / 1000));
  return sec + 's';
}

function syncUsageCooldownTicker(hasActiveCooldown) {
  if (hasActiveCooldown) {
    if (!usageCooldownTicker) {
      usageCooldownTicker = setInterval(() => {
        if (curSection !== 'usage' || !usageData) return;
        renderUsage();
      }, 1000);
    }
    return;
  }
  if (usageCooldownTicker) {
    clearInterval(usageCooldownTicker);
    usageCooldownTicker = null;
  }
}

function renderUsageHeaders() {
  document.getElementById('usage-title').textContent = t('usageMonitor');
  document.getElementById('usage-refresh-btn').textContent = t('refreshUsage');
  document.getElementById('usage-date-label').textContent = t('usageDate');
  document.getElementById('error-title').textContent = t('errorLogs');
  const dateInput = document.getElementById('usage-date');
  if (!dateInput.value) {
    dateInput.value = new Date().toISOString().slice(0, 10);
  }
}

async function loadUsage() {
  const date = document.getElementById('usage-date').value || new Date().toISOString().slice(0, 10);
  const [data, errData] = await Promise.all([api('/usage?date=' + date), api('/errors?date=' + date)]);
  if (data) { usageData = data; renderUsage(); }
  renderErrors(errData);
}

function renderUsage() {
  const container = document.getElementById('usage-container');
  if (!usageData || !usageData.channels || usageData.channels.length === 0) {
    container.innerHTML = '<div class="empty">' + t('noQuotaChannels') + '</div>';
    syncUsageCooldownTicker(false);
    return;
  }

  // 按有无用量排序：有用量的渠道排在前面
  const sorted = [...usageData.channels].sort((a, b) => {
    const aTotal = (a.keys || []).reduce((s, k) => s + (k.usage?.total || 0), 0);
    const bTotal = (b.keys || []).reduce((s, k) => s + (k.usage?.total || 0), 0);
    return bTotal - aTotal;
  });

  let hasAnyActiveCooldown = false;
  container.innerHTML = sorted.map(ch => {
    const statusDot = ch.enabled
      ? '<span style="width:8px;height:8px;border-radius:50%;background:var(--success);display:inline-block"></span>'
      : '<span style="width:8px;height:8px;border-radius:50%;background:var(--danger);display:inline-block"></span>';

    const allKeys = ch.keys || [];
    const totalKeys = allKeys.length;
    const totalPages = Math.max(1, Math.ceil(totalKeys / USAGE_PAGE_SIZE));
    const curPage = Math.min(usageKeyPages[ch.channel_id] || 1, totalPages);
    usageKeyPages[ch.channel_id] = curPage;

    const startIdx = (curPage - 1) * USAGE_PAGE_SIZE;
    const pageKeys = allKeys.slice(startIdx, startIdx + USAGE_PAGE_SIZE);

    const keyCards = pageKeys.map(k => {
      const u = k.usage;
      const rateState = k.rate_state || {};
      const cooldowns = rateState.cooldowns || {};
      const activeCooldownItems = Object.entries(cooldowns)
        .filter(([, until]) => Number(until) > Date.now())
        .sort((a, b) => Number(a[1]) - Number(b[1]));
      if (activeCooldownItems.length > 0) hasAnyActiveCooldown = true;

      const cooldownHtml = activeCooldownItems.length > 0
        ? '<div style="margin:8px 0 12px 0;font-size:12px;color:#b45309;background:#fffbeb;border:1px solid #fcd34d;border-radius:6px;padding:8px">' +
            '<div style="font-weight:600;margin-bottom:4px">' + t('cooldown') + '</div>' +
            activeCooldownItems.map(([m, until]) => {
              const modelTag = m === '*' ? 'all' : esc(m);
              return '<div style="display:flex;justify-content:space-between;gap:8px">' +
                '<span style="font-family:monospace">' + modelTag + '</span>' +
                '<span>' + formatCooldownLeft(Number(until)) + '</span>' +
              '</div>';
            }).join('') +
          '</div>'
        : '';

      const modelNames = Object.keys(u.models || {}).sort();

      const modelRows = modelNames.map(m => {
        const count = u.models[m] || 0;
        return '<div class="usage-row">' +
          '<div class="usage-label"><span style="font-family:monospace;font-size:12px">' + esc(m) + '</span><span class="usage-val">' + count + '</span></div>' +
          '<div class="progress-bar"><div class="progress-fill progress-ok" style="width:' + Math.min(count / 5, 100) + '%"></div></div>' +
          '</div>';
      }).join('');

      return '<div style="background:var(--bg-1);border:1px solid var(--border);border-radius:6px;padding:16px;margin-bottom:10px">' +
        '<div style="font-family:monospace;font-size:13px;color:var(--primary);margin-bottom:10px">' + esc(k.key_hint) + '</div>' +
        cooldownHtml +
        '<div class="usage-row">' +
          '<div class="usage-label"><span>' + t('totalUsage') + '</span><span class="usage-val">' + u.total + '</span></div>' +
          '<div class="progress-bar"><div class="progress-fill progress-ok" style="width:' + Math.min(u.total / 20, 100) + '%"></div></div>' +
        '</div>' +
        (modelRows
          ? '<div style="margin-top:12px"><div style="font-size:12px;color:var(--text-2);margin-bottom:8px;text-transform:uppercase;letter-spacing:.5px">' + t('perModelUsage') + '</div>' + modelRows + '</div>'
          : '<div style="margin-top:8px;font-size:12px;color:var(--text-2)">' + t('noModelUsageYet') + '</div>') +
      '</div>';
    }).join('');

    // 分页控件（仅在超过一页时显示）
    let paginationHtml = '';
    if (totalPages > 1) {
      const cid = ch.channel_id;
      const prevDisabled = curPage <= 1 ? ' disabled' : '';
      const nextDisabled = curPage >= totalPages ? ' disabled' : '';

      // 页码按钮（最多显示5个，当前页居中）
      let pageStart = Math.max(1, curPage - 2);
      let pageEnd = Math.min(totalPages, pageStart + 4);
      if (pageEnd - pageStart < 4) pageStart = Math.max(1, pageEnd - 4);

      let pageButtons = '';
      for (let p = pageStart; p <= pageEnd; p++) {
        const activeClass = p === curPage ? ' pg-active' : '';
        pageButtons += '<button class="' + activeClass + '" onclick="window._usagePage(\\\'' + cid + '\\\',' + p + ')">' + p + '</button>';
      }

      paginationHtml = '<div class="pagination">' +
        '<button' + prevDisabled + ' onclick="window._usagePage(\\\'' + cid + '\\\',' + (curPage - 1) + ')">&laquo;</button>' +
        pageButtons +
        '<button' + nextDisabled + ' onclick="window._usagePage(\\\'' + cid + '\\\',' + (curPage + 1) + ')">&raquo;</button>' +
        '<span class="pg-info">' + totalKeys + ' ' + t('keysTotal') + '</span>' +
      '</div>';
    }

    return '<div class="usage-card">' +
      '<h4>' + statusDot + ' ' + esc(ch.channel_name) + '</h4>' +
      keyCards +
      paginationHtml +
    '</div>';
  }).join('');
  syncUsageCooldownTicker(hasAnyActiveCooldown);
}

// 分页跳转
window._usagePage = function(channelId, page) {
  usageKeyPages[channelId] = page;
  renderUsage();
};

function renderErrors(errData) {
  const container = document.getElementById('error-container');
  if (!errData || !errData.channels || errData.channels.length === 0) {
    container.innerHTML = '<div class="empty">' + t('noErrors') + '</div>';
    return;
  }
  container.innerHTML = errData.channels.map(ch => {
    const errors = (ch.errors || []).slice().reverse();
    const rows = errors.map(e => {
      const time = e.time ? new Date(e.time).toLocaleTimeString() : '-';
      const statusBadge = e.status >= 500
        ? '<span class="badge badge-off">' + e.status + '</span>'
        : e.status === 404
          ? '<span class="badge" style="background:rgba(245,158,11,.12);color:var(--warning)">' + e.status + '</span>'
          : e.status > 0
            ? '<span class="badge" style="background:rgba(99,102,241,.12);color:var(--primary)">' + e.status + '</span>'
            : '<span class="badge badge-off">ERR</span>';
      return '<tr>' +
        '<td style="white-space:nowrap;font-size:13px;color:var(--text-2)">' + time + '</td>' +
        '<td style="font-family:monospace;font-size:13px">' + esc(e.model || '-') + '</td>' +
        '<td>' + statusBadge + '</td>' +
        '<td class="cell-truncate" title="' + esc(e.message) + '" style="font-size:13px">' + esc(e.message) + '</td>' +
        '<td style="font-family:monospace;font-size:12px;color:var(--text-2)">' + esc(e.key_hint || '-') + '</td>' +
      '</tr>';
    }).join('');

    return '<div class="usage-card">' +
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
          '<th>' + t('errorKey') + '</th>' +
        '</tr></thead><tbody>' + rows + '</tbody></table>' +
      '</div>' +
    '</div>';
  }).join('');
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
function fmtDate(d) { return d ? new Date(d).toLocaleDateString() : '-'; }
function fmtNum(n) { return n >= 1000000 ? (n / 1000000).toFixed(1) + 'M' : n >= 1000 ? (n / 1000).toFixed(1) + 'K' : String(n); }
function fmtCost(n) { return n >= 0.01 ? '$' + n.toFixed(2) : n > 0 ? '$' + n.toFixed(4) : '$0'; }

// 模型定价（每百万 token 美元）
const MODEL_PRICING = {
  'gpt-4o':{i:2.5,o:10},'gpt-4o-mini':{i:.15,o:.6},'gpt-4-turbo':{i:10,o:30},'gpt-4':{i:30,o:60},
  'gpt-3.5-turbo':{i:.5,o:1.5},'o1':{i:15,o:60},'o1-mini':{i:3,o:12},'o3-mini':{i:1.1,o:4.4},
  'claude-opus-4':{i:15,o:75},'claude-sonnet-4':{i:3,o:15},'claude-3-7-sonnet':{i:3,o:15},
  'claude-3-5-sonnet':{i:3,o:15},'claude-3-5-haiku':{i:.8,o:4},'claude-3-opus':{i:15,o:75},
  'claude-3-sonnet':{i:3,o:15},'claude-3-haiku':{i:.25,o:1.25},
  'deepseek-chat':{i:.14,o:.28},'deepseek-reasoner':{i:.55,o:2.19},
  'gemini-2.0-flash':{i:.1,o:.4},'gemini-2.0-pro':{i:1.25,o:10},'gemini-1.5-pro':{i:1.25,o:5},'gemini-1.5-flash':{i:.075,o:.3},
  'glm-4':{i:1,o:1},'glm-4-flash':{i:.01,o:.01},'glm-4-plus':{i:.5,o:.5},
  'qwen-turbo':{i:.3,o:.6},'qwen-plus':{i:.8,o:2},'qwen-max':{i:2,o:6},
};
function calcCost(model, pt, ct) {
  let p = MODEL_PRICING[model];
  if (!p) { for (const [k,v] of Object.entries(MODEL_PRICING)) { if (model && model.startsWith(k)) { p = v; break; } } }
  if (!p) return 0;
  return (pt * p.i + ct * p.o) / 1e6;
}
function calcKeyTotalCost(usage) {
  if (!usage || !usage.models) return 0;
  let total = 0;
  for (const [m, d] of Object.entries(usage.models)) {
    total += calcCost(m, d.prompt_tokens || 0, d.completion_tokens || 0);
  }
  return total;
}
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
