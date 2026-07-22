package admin

// indexHTML is the embedded WebUI page.
// Design: Light theme — white background, green accent, Chinese UI.
// No external dependencies. No emoji. No SVG icons.
const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Qoder-2API-Go 管理面板</title>
<style>
:root {
  --bg: #F9FAFB;
  --surface: #FFFFFF;
  --surface-2: #F3F4F6;
  --surface-3: #ECFDF5;
  --border: #E5E7EB;
  --border-light: #F0F1F3;
  --text: #111827;
  --text-secondary: #374151;
  --muted: #6B7280;
  --accent: #10B981;
  --accent-hover: #059669;
  --accent-light: #ECFDF5;
  --accent-border: #A7F3D0;
  --danger: #EF4444;
  --danger-hover: #DC2626;
  --danger-light: #FEF2F2;
  --danger-border: #FECACA;
  --warning: #F59E0B;
  --warning-light: #FFFBEB;
  --warning-border: #FDE68A;
  --radius: 16px;
  --radius-sm: 12px;
  --shadow: 0 1px 3px rgba(0,0,0,0.06);
  --shadow-md: 0 4px 6px -1px rgba(0,0,0,0.06), 0 2px 4px -2px rgba(0,0,0,0.04);
  --shadow-lg: 0 10px 15px -3px rgba(0,0,0,0.06), 0 4px 6px -4px rgba(0,0,0,0.04);
}

* { margin: 0; padding: 0; box-sizing: border-box; }

body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
    "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Helvetica, Arial,
    sans-serif;
  background: var(--bg);
  color: var(--text);
  line-height: 1.6;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}

code, .mono {
  font-family: "SF Mono", "Fira Code", "Fira Mono", "Roboto Mono",
    Monaco, Consolas, "Courier New", monospace;
}

.hidden { display: none !important; }

/* --- Login --- */
.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #ECFDF5 0%, #F9FAFB 50%, #F0FDF4 100%);
  padding: 20px;
}
.login-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 20px;
  box-shadow: var(--shadow-lg);
  padding: 48px 40px;
  width: 100%;
  max-width: 400px;
}
.login-title {
  text-align: center;
  font-size: 24px;
  font-weight: 700;
  color: var(--text);
  margin-bottom: 8px;
  letter-spacing: -0.02em;
}
.login-subtitle {
  text-align: center;
  font-size: 14px;
  color: var(--muted);
  margin-bottom: 28px;
}
.login-input {
  width: 100%;
  padding: 14px 16px;
  border: 1px solid var(--border);
  border-radius: 12px;
  font-size: 15px;
  font-family: inherit;
  background: var(--surface);
  color: var(--text);
  outline: none;
  transition: border-color 0.2s, box-shadow 0.2s;
}
.login-input:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgba(16,185,129,0.1);
}
.login-btn {
  width: 100%;
  padding: 14px;
  background: var(--accent);
  color: #fff;
  border: none;
  border-radius: 12px;
  font-size: 15px;
  font-weight: 600;
  cursor: pointer;
  margin-top: 16px;
  transition: background 0.2s, transform 0.1s;
  font-family: inherit;
}
.login-btn:hover {
  background: var(--accent-hover);
  transform: translateY(-1px);
}
.login-btn:active {
  transform: translateY(0);
}
.login-error {
  color: var(--danger);
  font-size: 13px;
  text-align: center;
  margin-top: 12px;
  min-height: 18px;
}

/* --- Dashboard --- */
.container {
  max-width: 920px;
  margin: 0 auto;
  padding: 24px 20px 60px;
}

.header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 20px 0;
  margin-bottom: 28px;
}
.header-left {
  display: flex;
  align-items: center;
  gap: 16px;
}
.header h1 {
  font-size: 24px;
  font-weight: 700;
  letter-spacing: -0.02em;
  color: var(--text);
}
.header .version {
  font-size: 12px;
  color: var(--muted);
  background: var(--surface-2);
  padding: 2px 8px;
  border-radius: 4px;
  font-family: "SF Mono", monospace;
}

/* --- Tabs --- */
.tabs {
  display: flex;
  gap: 4px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 4px;
  margin-bottom: 28px;
  box-shadow: var(--shadow);
}
.tab {
  flex: 1;
  text-align: center;
  padding: 12px 20px;
  font-size: 14px;
  font-weight: 500;
  color: var(--muted);
  background: transparent;
  border: none;
  border-radius: 10px;
  cursor: pointer;
  transition: all 0.2s;
  font-family: inherit;
}
.tab:hover { color: var(--text-secondary); }
.tab.active {
  color: var(--accent-hover);
  background: var(--accent-light);
  font-weight: 600;
}

/* --- Cards --- */
.card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 16px;
  margin-bottom: 24px;
  overflow: hidden;
  box-shadow: var(--shadow);
}
.card-header {
  padding: 18px 24px;
  border-bottom: 1px solid var(--border-light);
  font-size: 16px;
  font-weight: 600;
  color: var(--text);
}
.card-body { padding: 20px; }

/* --- Form --- */
.row {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-bottom: 14px;
}
.row > label {
  min-width: 90px;
  font-weight: 500;
  font-size: 14px;
  color: var(--text-secondary);
}
.row > input { flex: 1; }

input[type="text"], input[type="number"], input[type="password"] {
  padding: 12px 16px;
  width: 100%;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 12px;
  color: var(--text);
  font-size: 14px;
  font-family: inherit;
  outline: none;
  transition: border-color 0.2s, box-shadow 0.2s;
}
input[type="text"]:focus, input[type="number"]:focus, input[type="password"]:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgba(16,185,129,0.1);
}
input::placeholder { color: #9CA3AF; }
input[readonly] {
  background: var(--surface-2);
  color: var(--muted);
}

/* --- Buttons --- */
.btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 12px 24px;
  background: var(--accent);
  color: #fff;
  border: none;
  border-radius: 12px;
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
  transition: background 0.2s, transform 0.1s;
  font-family: inherit;
}
.btn:hover {
  background: var(--accent-hover);
  transform: translateY(-1px);
}
.btn:active {
  transform: translateY(0);
}
.btn.secondary {
  background: var(--surface);
  color: var(--text-secondary);
  border: 1px solid var(--border);
}
.btn.secondary:hover {
  background: var(--surface-2);
  border-color: var(--muted);
  transform: translateY(-1px);
}
.btn.danger {
  background: var(--surface);
  color: var(--danger);
  border: 1px solid var(--danger-border);
}
.btn.danger:hover {
  background: var(--danger-light);
  transform: translateY(-1px);
}
.btn.sm {
  padding: 8px 16px;
  font-size: 13px;
  font-weight: 500;
}
.btn-row { display: flex; gap: 8px; }

/* --- Table --- */
table { width: 100%; border-collapse: collapse; }
th {
  padding: 10px 16px;
  text-align: left;
  font-size: 12px;
  font-weight: 600;
  color: var(--muted);
  background: var(--surface-2);
  border-bottom: 1px solid var(--border);
}
td {
  padding: 12px 16px;
  border-bottom: 1px solid var(--border-light);
  font-size: 14px;
  color: var(--text-secondary);
}
tr:last-child td { border-bottom: none; }
td .mono { font-size: 13px; color: var(--accent-hover); }

/* --- Model grid --- */
.model-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 10px;
}
.model-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 18px;
  background: var(--surface-2);
  border: 1px solid var(--border-light);
  border-radius: 12px;
  font-size: 13px;
  transition: border-color 0.2s, transform 0.1s;
}
.model-item:hover {
  border-color: var(--accent-border);
  transform: translateY(-1px);
}

/* --- Toast --- */
.toast {
  position: fixed;
  bottom: 28px;
  right: 28px;
  padding: 16px 24px;
  background: var(--surface);
  border: 1px solid var(--accent-border);
  border-radius: 14px;
  color: var(--text);
  font-size: 14px;
  font-weight: 500;
  z-index: 9999;
  box-shadow: var(--shadow-lg);
  transition: opacity 0.3s, transform 0.3s;
}
.toast.error { border-color: var(--danger-border); }
.toast.hidden { opacity: 0; transform: translateY(10px); pointer-events: none; }

/* --- Hints --- */
.hint {
  font-size: 13px;
  color: var(--muted);
  margin-bottom: 16px;
}
.hint-warn {
  font-size: 13px;
  color: #92400E;
  padding: 12px 16px;
  background: var(--warning-light);
  border: 1px solid var(--warning-border);
  border-radius: 12px;
  margin-top: 12px;
}
.empty-row {
  text-align: center;
  padding: 28px;
  color: var(--muted);
  font-size: 14px;
}

/* --- Section divider --- */
.section-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
  margin-bottom: 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--border-light);
}

/* --- Logout button --- */
.logout-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 10px 20px;
  background: var(--surface);
  color: var(--text-secondary);
  border: 1px solid var(--border);
  border-radius: 12px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s, transform 0.1s;
  font-family: inherit;
}
.logout-btn:hover {
  background: var(--danger-light);
  color: var(--danger);
  border-color: var(--danger-border);
  transform: translateY(-1px);
}


@media (max-width: 640px) {
  .row { flex-direction: column; align-items: stretch; }
  .row > label { min-width: auto; }
  .header h1 { font-size: 17px; }
  .tabs { flex-wrap: wrap; }
}
</style>
</head>
<body>

<!-- Login -->
<div id="loginPage" class="login-wrap">
  <div class="login-card">
    <div class="login-title">Qoder-2API-Go</div>
    <div class="login-subtitle">请输入管理密码以继续</div>
    <input type="password" id="loginPassword" class="login-input" placeholder="密码" autofocus>
    <button class="login-btn" type="button" onclick="doLogin()">登 录</button>
    <div id="loginError" class="login-error"></div>
  </div>
</div>

<!-- Dashboard -->
<div id="dashboard" class="hidden">
<div class="container">
  <div class="header">
    <div class="header-left">
      <h1>Qoder-2API-Go</h1>
    </div>
    <button class="logout-btn" type="button" onclick="doLogout()">
      退出
    </button>
  </div>

  <div class="tabs">
    <button class="tab active" data-tab="keys" onclick="switchTab(event,'keys')">密钥</button>
    <button class="tab" data-tab="token" onclick="switchTab(event,'token')">令牌</button>
    <button class="tab" data-tab="models" onclick="switchTab(event,'models')">模型</button>
    <button class="tab" data-tab="config" onclick="switchTab(event,'config')">设置</button>
  </div>

  <!-- API Keys -->
  <div id="tab-keys" class="tab-panel">
    <div class="card">
      <div class="card-header">
        创建 API Key
      </div>
      <div class="card-body">
        <div class="row">
          <label>密钥</label>
          <input type="text" id="newKey" placeholder="留空则自动生成">
        </div>
        <div class="row">
          <label>备注</label>
          <input type="text" id="newNote" placeholder="可选描述">
        </div>
        <div class="row">
          <label></label>
          <div class="btn-row">
            <button class="btn" type="button" onclick="addKey()">创建</button>
            <button class="btn secondary" type="button" onclick="addKey(true)">随机生成</button>
          </div>
        </div>
      </div>
    </div>
    <div class="card">
      <div class="card-header">
        密钥列表
      </div>
      <div class="card-body" style="padding:0">
        <table>
          <thead><tr><th>密钥</th><th>备注</th><th>创建时间</th><th>操作</th></tr></thead>
          <tbody id="keysTable"></tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- Token -->
  <div id="tab-token" class="tab-panel hidden">
    <div class="card">
      <div class="card-header">
        Qoder 访问令牌
      </div>
      <div class="card-body">
        <p class="hint">Qoder 的个人访问令牌，API 运行必须配置此项。</p>
        <div class="row">
          <label>当前令牌</label>
          <input type="text" id="patDisplay" readonly>
        </div>
        <div class="row">
          <label>新令牌</label>
          <input type="text" id="patInput" placeholder="pt-...">
        </div>
        <div class="row">
          <label></label>
          <button class="btn" type="button" onclick="savePAT()">保存</button>
        </div>
      </div>
    </div>
  </div>

  <!-- Models -->
  <div id="tab-models" class="tab-panel hidden">
    <div class="card">
      <div class="card-header">
        可用模型
      </div>
      <div class="card-body">
        <p class="hint">点击「复制」获取模型名称用于 API 调用。</p>
        <div id="modelsList" class="model-grid"></div>
      </div>
    </div>
  </div>

  <!-- Settings -->
  <div id="tab-config" class="tab-panel hidden">
    <div class="card">
      <div class="card-header">
        服务器配置
      </div>
      <div class="card-body">
        <p class="hint">配置监听地址和端口，修改后需要重启服务才能生效。</p>
        <div class="row">
          <label>主机</label>
          <input type="text" id="cfgHost" placeholder="0.0.0.0">
        </div>
        <div class="row">
          <label>端口</label>
          <input type="number" id="cfgPort" placeholder="18080">
        </div>
        <div class="row">
          <label></label>
          <button class="btn" type="button" onclick="saveConfig()">保存</button>
        </div>
        <div id="configHint" class="hint-warn hidden"></div>
      </div>
    </div>

    <div class="card">
      <div class="card-header">
        修改密码
      </div>
      <div class="card-body">
        <p class="hint">修改管理面板的登录密码。</p>
        <div class="row">
          <label>当前密码</label>
          <input type="password" id="curPwd" placeholder="当前密码">
        </div>
        <div class="row">
          <label>新密码</label>
          <input type="password" id="newPwd" placeholder="新密码">
        </div>
        <div class="row">
          <label></label>
          <button class="btn" type="button" onclick="savePassword()">修改密码</button>
        </div>
      </div>
    </div>
  </div>
</div>
</div>

<div id="toast" class="toast hidden"></div>

<script>
/* --- Auth --- */

function api(path, opts) {
  opts = opts || {};
  return fetch('/admin/api' + path, {
    method: opts.method || 'GET',
    body: opts.body,
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin'
  }).then(function(res) {
    return res.json().catch(function() { return {}; }).then(function(data) {
      if (!res.ok) throw new Error(data.error || '请求失败 (' + res.status + ')');
      return data;
    });
  });
}

function checkAuth() {
  api('/auth').then(function(data) {
    if (data.authenticated) {
      showDashboard();
    } else {
      showLogin();
    }
  }).catch(function() { showLogin(); });
}

function showLogin() {
  document.getElementById('loginPage').classList.remove('hidden');
  document.getElementById('dashboard').classList.add('hidden');
}

function showDashboard() {
  document.getElementById('loginPage').classList.add('hidden');
  document.getElementById('dashboard').classList.remove('hidden');
  loadKeys();
}

function doLogin() {
  var pwd = document.getElementById('loginPassword').value;
  if (!pwd) { return; }
  api('/login', { method: 'POST', body: JSON.stringify({ password: pwd }) })
    .then(function() {
      document.getElementById('loginPassword').value = '';
      document.getElementById('loginError').textContent = '';
      showDashboard();
    })
    .catch(function(e) {
      document.getElementById('loginError').textContent = e.message;
    });
}

function doLogout() {
  api('/logout', { method: 'POST' })
    .then(function() { showLogin(); })
    .catch(function() { showLogin(); });
}

document.getElementById('loginPassword').addEventListener('keydown', function(e) {
  if (e.key === 'Enter') doLogin();
});

/* --- Toast --- */

function showToast(msg, type) {
  type = type || 'success';
  var t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'toast ' + type;
  setTimeout(function() { t.className = 'toast hidden'; }, 3000);
}

function escapeHtml(s) {
  var d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML.replace(/'/g, '&#39;');
}

/* --- Tabs --- */

function switchTab(e, name) {
  document.querySelectorAll('.tab-panel').forEach(function(el) { el.classList.add('hidden'); });
  document.querySelectorAll('.tab').forEach(function(el) { el.classList.remove('active'); });
  document.getElementById('tab-' + name).classList.remove('hidden');
  if (e && e.currentTarget) { e.currentTarget.classList.add('active'); }
  if (name === 'keys') loadKeys();
  if (name === 'token') loadPAT();
  if (name === 'models') loadModels();
  if (name === 'config') loadConfig();
}

/* --- API Keys --- */

function loadKeys() {
  api('/keys').then(function(data) {
    var tb = document.getElementById('keysTable');
    tb.innerHTML = '';
    if (!data.keys || data.keys.length === 0) {
      tb.innerHTML = '<tr><td colspan="4" class="empty-row">暂无 Key，请在上方创建。</td></tr>';
      return;
    }
    data.keys.forEach(function(k) {
      var tr = document.createElement('tr');
      var dt = new Date(k.created_at * 1000).toLocaleDateString('zh-CN');
      tr.innerHTML = '<td><span class="mono">' + escapeHtml(k.key) + '</span></td>' +
        '<td>' + escapeHtml(k.note || '') + '</td>' +
        '<td style="color:var(--muted)">' + dt + '</td>' +
        '<td><div class="btn-row">' +
        '<button class="btn secondary sm" type="button" onclick="copyText(\'' + escapeHtml(k.key) + '\')">复制</button>' +
        '<button class="btn danger sm" type="button" onclick="deleteKey(\'' + k.id + '\')">删除</button>' +
        '</div></td>';
      tb.appendChild(tr);
    });
  }).catch(function(e) { showToast(e.message, 'error'); });
}

function addKey(gen) {
  var key = gen ? '' : document.getElementById('newKey').value.trim();
  var note = document.getElementById('newNote').value.trim();
  api('/keys', { method: 'POST', body: JSON.stringify({ key: key, note: note }) })
    .then(function() {
      document.getElementById('newKey').value = '';
      document.getElementById('newNote').value = '';
      showToast('Key 已添加');
      loadKeys();
    })
    .catch(function(e) { showToast(e.message, 'error'); });
}

function deleteKey(id) {
  if (!confirm('确定删除此 Key？')) return;
  api('/keys?id=' + id, { method: 'DELETE' })
    .then(function() { showToast('Key 已删除'); loadKeys(); })
    .catch(function(e) { showToast(e.message, 'error'); });
}

/* --- PAT --- */

function loadPAT() {
  api('/pat').then(function(data) {
    document.getElementById('patDisplay').value = data.pat_masked || '';
    document.getElementById('patInput').value = '';
  }).catch(function(e) { showToast(e.message, 'error'); });
}

function savePAT() {
  var pat = document.getElementById('patInput').value.trim();
  if (!pat) { showToast('PAT 不能为空', 'error'); return; }
  api('/pat', { method: 'POST', body: JSON.stringify({ pat: pat }) })
    .then(function() { showToast('PAT 已保存'); loadPAT(); })
    .catch(function(e) { showToast(e.message, 'error'); });
}

/* --- Models --- */

function loadModels() {
  api('/models').then(function(data) {
    var g = document.getElementById('modelsList');
    g.innerHTML = '';
    if (!data.models || data.models.length === 0) {
      g.innerHTML = '<p style="color:var(--muted)">暂无可用模型</p>';
      return;
    }
    data.models.forEach(function(m) {
      var d = document.createElement('div');
      d.className = 'model-item';
      d.innerHTML = '<span>' + escapeHtml(m) + '</span><button class="btn secondary sm" type="button" onclick="copyText(\'' + escapeHtml(m) + '\')">复制</button>';
      g.appendChild(d);
    });
  }).catch(function(e) { showToast(e.message, 'error'); });
}

/* --- Config --- */

function loadConfig() {
  api('/config').then(function(data) {
    document.getElementById('cfgHost').value = data.host || '';
    document.getElementById('cfgPort').value = data.port || '';
    document.getElementById('configHint').classList.add('hidden');
  }).catch(function(e) { showToast(e.message, 'error'); });
}

function saveConfig() {
  var host = document.getElementById('cfgHost').value.trim();
  var port = parseInt(document.getElementById('cfgPort').value, 10);
  if (!host) host = '0.0.0.0';
  if (!port || port < 1 || port > 65535) { showToast('端口无效', 'error'); return; }
  api('/config', { method: 'POST', body: JSON.stringify({ host: host, port: port }) })
    .then(function(data) {
      showToast('配置已保存，重启后生效');
      var h = document.getElementById('configHint');
      h.textContent = data.restart || '修改后需要重启服务才能生效。';
      h.classList.remove('hidden');
      loadConfig();
    })
    .catch(function(e) { showToast(e.message, 'error'); });
}

/* --- Password --- */

function savePassword() {
  var cur = document.getElementById('curPwd').value;
  var nw = document.getElementById('newPwd').value;
  if (!cur || !nw) { showToast('请填写完整', 'error'); return; }
  api('/password', { method: 'POST', body: JSON.stringify({ current_password: cur, new_password: nw }) })
    .then(function() {
      showToast('密码已修改');
      document.getElementById('curPwd').value = '';
      document.getElementById('newPwd').value = '';
    })
    .catch(function(e) { showToast(e.message, 'error'); });
}

/* ── Clipboard ── */

function copyText(text) {
  navigator.clipboard.writeText(text).then(function() { showToast('已复制到剪贴板'); });
}

/* ── Init ── */

checkAuth();
</script>
</body>
</html>`
