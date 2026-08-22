package admin

// indexHTML is the embedded WebUI page.
// Design: editorial theme with light/dark variants — warm paper neutrals,
// ink primary, hairline borders, tabular numerals. No external dependencies.
// No emoji.
const indexHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Qoder-2API 管理控制台</title>
<script>
try {
  var t = localStorage.getItem('qoder2api-theme');
  var dark = t === 'dark' || (t !== 'light' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
} catch (e) {}
</script>
<style>
:root {
  color-scheme: light;
  --bg: #FAFAF9;
  --surface: #FFFFFF;
  --surface-2: #F5F5F4;
  --surface-3: #F0F0EE;
  --border: #E7E5E4;
  --border-strong: #D6D3D1;
  --text: #1C1917;
  --text-2: #44403C;
  --muted: #78716C;
  --accent: #2563EB;
  --accent-hover: #1D4ED8;
  --accent-soft: #EFF6FF;
  --accent-border: #BFDBFE;
  --accent-glow: rgba(37,99,235,0.12);
  --success: #15803D;
  --success-soft: #F0FDF4;
  --success-border: #BBF7D0;
  --success-glow: rgba(21,128,61,0.12);
  --danger: #B91C1C;
  --danger-soft: #FEF2F2;
  --danger-border: #FECACA;
  --warning: #92400E;
  --warning-soft: #FFFBEB;
  --warning-border: #FDE68A;
  --mark-bg: #1C1917;
  --mark-fg: #FFFFFF;
  --btn-bg: #1C1917;
  --btn-fg: #FFFFFF;
  --btn-bg-hover: #3B3734;
  --nav-glass: rgba(255,255,255,0.92);
  --login-glow: #F1F0ED;
  --placeholder: #A8A29E;
  --radius: 14px;
  --radius-sm: 10px;
  --shadow: 0 1px 2px rgba(28,25,23,0.05);
  --shadow-md: 0 4px 12px -2px rgba(28,25,23,0.08);
  --shadow-lg: 0 16px 40px -12px rgba(28,25,23,0.16);
}

[data-theme="dark"] {
  color-scheme: dark;
  --bg: #131211;
  --surface: #1B1A18;
  --surface-2: #23211F;
  --surface-3: #2C2A27;
  --border: #2E2C29;
  --border-strong: #45413C;
  --text: #EDEAE6;
  --text-2: #C4BFB8;
  --muted: #8F8A82;
  --accent: #6D9CF0;
  --accent-hover: #8BB0F4;
  --accent-soft: rgba(109,156,240,0.14);
  --accent-border: rgba(109,156,240,0.45);
  --accent-glow: rgba(109,156,240,0.28);
  --success: #4ADE80;
  --success-soft: rgba(74,222,128,0.12);
  --success-border: rgba(74,222,128,0.4);
  --success-glow: rgba(74,222,128,0.22);
  --danger: #F87171;
  --danger-soft: rgba(248,113,113,0.12);
  --danger-border: rgba(248,113,113,0.4);
  --warning: #FBBF24;
  --warning-soft: rgba(251,191,36,0.12);
  --warning-border: rgba(251,191,36,0.4);
  --mark-bg: #EDEAE6;
  --mark-fg: #1C1917;
  --btn-bg: #EDEAE6;
  --btn-fg: #1C1917;
  --btn-bg-hover: #FBFAF8;
  --nav-glass: rgba(27,26,24,0.92);
  --login-glow: #1F1D1B;
  --placeholder: #6E6A64;
  --shadow: 0 1px 2px rgba(0,0,0,0.3);
  --shadow-md: 0 4px 12px -2px rgba(0,0,0,0.4);
  --shadow-lg: 0 16px 40px -12px rgba(0,0,0,0.6);
}

* { margin: 0; padding: 0; box-sizing: border-box; }

html { -webkit-text-size-adjust: 100%; }

body {
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC",
    "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Helvetica, Arial,
    sans-serif;
  background: var(--bg);
  color: var(--text);
  line-height: 1.55;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
  font-size: 14px;
}

code, .mono, .num {
  font-family: "SF Mono", "Fira Code", "Roboto Mono", "Cascadia Mono",
    Consolas, "Courier New", monospace;
}

.num { font-variant-numeric: tabular-nums; }

.hidden { display: none !important; }

::selection { background: var(--accent-border); }

/* ---------- Login ---------- */

.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background:
    radial-gradient(1200px 600px at 50% -10%, var(--login-glow) 0%, transparent 60%),
    var(--bg);
}
.login-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 18px;
  box-shadow: var(--shadow-lg);
  padding: 44px 40px 36px;
  width: 100%;
  max-width: 380px;
}
.login-mark {
  width: 44px; height: 44px;
  background: var(--mark-bg);
  border-radius: 12px;
  display: flex; align-items: center; justify-content: center;
  margin: 0 auto 18px;
  color: var(--mark-fg);
  font-family: Georgia, "Songti SC", serif;
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.02em;
}
.login-title {
  text-align: center;
  font-size: 19px;
  font-weight: 650;
  letter-spacing: -0.015em;
}
.login-subtitle {
  text-align: center;
  font-size: 13px;
  color: var(--muted);
  margin: 6px 0 28px;
}
.login-input {
  width: 100%;
  padding: 11px 14px;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  font-size: 14px;
  font-family: inherit;
  background: var(--surface);
  color: var(--text);
  outline: none;
  transition: border-color 0.15s, box-shadow 0.15s;
}
.login-input:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-glow);
}
.login-btn {
  width: 100%;
  padding: 11px;
  background: var(--btn-bg);
  color: var(--btn-fg);
  border: none;
  border-radius: var(--radius-sm);
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
  margin-top: 16px;
  font-family: inherit;
  transition: background 0.15s, transform 0.1s;
}
.login-btn:hover { background: var(--btn-bg-hover); }
.login-btn:active { transform: translateY(1px); }
.login-error {
  color: var(--danger);
  font-size: 13px;
  text-align: center;
  margin-top: 12px;
  min-height: 18px;
}

/* ---------- Shell ---------- */

.shell { display: flex; min-height: 100vh; }

.sidebar {
  width: 224px;
  flex-shrink: 0;
  background: var(--surface);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  position: sticky;
  top: 0;
  height: 100vh;
}
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 22px 20px 18px;
}
.brand-mark {
  width: 30px; height: 30px;
  background: var(--mark-bg);
  border-radius: 9px;
  display: flex; align-items: center; justify-content: center;
  color: var(--mark-fg);
  font-family: Georgia, "Songti SC", serif;
  font-size: 16px;
  font-weight: 700;
}
.brand-name {
  font-size: 15px;
  font-weight: 650;
  letter-spacing: -0.01em;
}
.brand-sub {
  font-size: 11px;
  color: var(--muted);
  letter-spacing: 0.02em;
}

.nav { flex: 1; padding: 8px 12px; }
.nav-label {
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.08em;
  padding: 0 10px 8px;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 9px 10px;
  margin-bottom: 2px;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  font-size: 13.5px;
  font-weight: 500;
  color: var(--text-2);
  cursor: pointer;
  font-family: inherit;
  text-align: left;
  transition: background 0.12s, color 0.12s;
}
.nav-item svg { flex-shrink: 0; color: var(--muted); }
.nav-item:hover { background: var(--surface-2); color: var(--text); }
.nav-item.active { background: var(--surface-2); color: var(--text); font-weight: 600; }
.nav-item.active svg { color: var(--text); }

.sidebar-foot {
  padding: 14px 16px 18px;
  border-top: 1px solid var(--border);
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.status {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  font-size: 12.5px;
  color: var(--muted);
}
.status-dot {
  width: 7px; height: 7px;
  border-radius: 50%;
  background: var(--success);
  box-shadow: 0 0 0 3px var(--success-glow);
}
.logout-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 7px 12px;
  background: transparent;
  color: var(--muted);
  border: 1px solid var(--border);
  border-radius: 8px;
  font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
  font-family: inherit;
  transition: all 0.12s;
}
.logout-btn:hover {
  color: var(--danger);
  background: var(--danger-soft);
  border-color: var(--danger-border);
}

.main {
  flex: 1;
  min-width: 0;
  padding: 34px 40px 64px;
}
.main-inner { max-width: 1040px; margin: 0 auto; }

.page-head { margin-bottom: 24px; }
.page-title {
  font-size: 20px;
  font-weight: 700;
  letter-spacing: -0.015em;
}
.page-desc {
  font-size: 13px;
  color: var(--muted);
  margin-top: 4px;
}

/* ---------- Stat cards ---------- */

.stat-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  margin-bottom: 14px;
}
.stat-card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 18px 20px;
  box-shadow: var(--shadow);
}
.stat-label {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 11.5px;
  font-weight: 600;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.07em;
  margin-bottom: 10px;
}
.stat-dot { width: 7px; height: 7px; border-radius: 50%; }
.stat-dot.total { background: var(--text); }
.stat-dot.success { background: var(--success); }
.stat-dot.failed { background: var(--danger); }
.stat-dot.rate { background: var(--accent); }
.stat-value {
  font-size: 27px;
  font-weight: 700;
  letter-spacing: -0.02em;
  line-height: 1.1;
}
.stat-sub {
  font-size: 12px;
  color: var(--muted);
  margin-top: 6px;
}

/* ---------- Cards ---------- */

.card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  margin-bottom: 16px;
  overflow: hidden;
  box-shadow: var(--shadow);
}
.card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
}
.card-title { font-size: 14.5px; font-weight: 650; }
.card-title small {
  display: block;
  font-size: 12px;
  font-weight: 400;
  color: var(--muted);
  margin-top: 2px;
}
.card-body { padding: 20px; }
.card-body.flush { padding: 0; overflow-x: auto; }

/* ---------- Chart ---------- */

.chart-head { display: flex; align-items: baseline; justify-content: space-between; }
.chart-legend {
  display: flex;
  align-items: center;
  gap: 16px;
  font-size: 12px;
  color: var(--muted);
}
.legend-item { display: inline-flex; align-items: center; gap: 6px; }
.legend-dot { width: 9px; height: 9px; border-radius: 2px; }
.legend-dot.ok { background: var(--accent); }
.legend-dot.fail { background: var(--danger); }
.chart-peak { font-size: 12px; color: var(--muted); }

.chart {
  overflow-x: auto;
}
.chart-plot {
  display: flex;
  align-items: stretch;
  gap: 6px;
  height: 150px;
  margin-top: 18px;
  padding-top: 8px;
  border-bottom: 1px solid var(--border);
}
.chart-col {
  flex: 1 1 0;
  min-width: 14px;
  display: flex;
  flex-direction: column;
  align-items: center;
  height: 100%;
}
.chart-bars {
  flex: 1;
  width: 100%;
  max-width: 18px;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  gap: 1px;
}
.bar { width: 100%; border-radius: 2px 2px 0 0; }
.bar.ok { background: var(--accent); }
.bar.fail { background: var(--danger); opacity: 0.85; }
.chart-axis {
  display: flex;
  gap: 6px;
  padding-top: 6px;
}
.chart-axis-item {
  flex: 1 1 0;
  min-width: 14px;
  text-align: center;
  font-size: 10px;
  color: var(--muted);
  font-family: "SF Mono", Consolas, monospace;
  white-space: nowrap;
}

/* ---------- Table ---------- */

table { width: 100%; border-collapse: collapse; }
th {
  padding: 10px 20px;
  text-align: left;
  font-size: 11px;
  font-weight: 600;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.07em;
  background: var(--surface-2);
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
td {
  padding: 12px 20px;
  border-bottom: 1px solid var(--border);
  font-size: 13.5px;
  color: var(--text-2);
  vertical-align: middle;
}
tr:last-child td { border-bottom: none; }
tbody tr { transition: background 0.1s; }
tbody tr:hover { background: var(--surface-2); }
td .mono { font-size: 12.5px; }
td .muted { color: var(--muted); }
td.num, th.num { text-align: right; }

.rate-cell { display: flex; align-items: center; gap: 10px; justify-content: flex-end; }
.rate-bar {
  width: 72px;
  height: 5px;
  background: var(--surface-3);
  border-radius: 3px;
  overflow: hidden;
}
.rate-fill { height: 100%; border-radius: 3px; background: var(--accent); }
.rate-fill.warn { background: var(--warning); }
.rate-fill.bad { background: var(--danger); }
.rate-text { font-size: 12.5px; color: var(--text-2); min-width: 44px; text-align: right; }

.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.badge.ok { background: var(--success-soft); color: var(--success); border: 1px solid var(--success-border); }
.badge.bad { background: var(--danger-soft); color: var(--danger); border: 1px solid var(--danger-border); }

/* ---------- Forms ---------- */

.row {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-bottom: 14px;
}
.row > label {
  min-width: 88px;
  font-weight: 500;
  font-size: 13.5px;
  color: var(--text-2);
}
.row > .field { flex: 1; }

input[type="text"], input[type="number"], input[type="password"] {
  width: 100%;
  padding: 10px 13px;
  background: var(--surface);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-sm);
  color: var(--text);
  font-size: 13.5px;
  font-family: inherit;
  outline: none;
  transition: border-color 0.15s, box-shadow 0.15s;
}
input[type="text"]:focus, input[type="number"]:focus, input[type="password"]:focus {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-glow);
}
input::placeholder { color: var(--placeholder); }
input[readonly] {
  background: var(--surface-2);
  color: var(--muted);
  border-color: var(--border);
}

/* ---------- Buttons ---------- */

.btn {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 10px 18px;
  background: var(--btn-bg);
  color: var(--btn-fg);
  border: none;
  border-radius: var(--radius-sm);
  font-size: 13.5px;
  font-weight: 600;
  cursor: pointer;
  transition: background 0.15s, transform 0.1s;
  font-family: inherit;
  white-space: nowrap;
}
.btn:hover { background: var(--btn-bg-hover); }
.btn:active { transform: translateY(1px); }
.btn.secondary {
  background: var(--surface);
  color: var(--text-2);
  border: 1px solid var(--border-strong);
  font-weight: 500;
}
.btn.secondary:hover { background: var(--surface-2); color: var(--text); }
.btn.danger {
  background: var(--surface);
  color: var(--danger);
  border: 1px solid var(--danger-border);
  font-weight: 500;
}
.btn.danger:hover { background: var(--danger-soft); }
.btn.sm {
  padding: 5px 11px;
  font-size: 12.5px;
  font-weight: 500;
  border-radius: 8px;
}
.btn-row { display: flex; gap: 8px; }
.btn-row.right { justify-content: flex-end; }

/* ---------- Model grid ---------- */

.model-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
  gap: 10px;
}
.model-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 12px 16px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  font-size: 13.5px;
  font-weight: 500;
  transition: border-color 0.15s, box-shadow 0.15s;
}
.model-item:hover {
  border-color: var(--border-strong);
  box-shadow: var(--shadow);
}

/* ---------- Hints ---------- */

.hint {
  font-size: 13px;
  color: var(--muted);
  margin-bottom: 16px;
}
.hint-warn {
  font-size: 13px;
  color: var(--warning);
  padding: 11px 14px;
  background: var(--warning-soft);
  border: 1px solid var(--warning-border);
  border-radius: var(--radius-sm);
  margin-top: 12px;
}
.empty-row {
  text-align: center;
  padding: 32px 16px;
  color: var(--muted);
  font-size: 13.5px;
}
.empty-row .empty-title { font-size: 14px; font-weight: 600; color: var(--text-2); margin-bottom: 4px; }

/* ---------- Toast ---------- */

.toast {
  position: fixed;
  bottom: 28px;
  left: 50%;
  transform: translateX(-50%) translateY(0);
  padding: 12px 22px;
  background: var(--btn-bg);
  color: var(--btn-fg);
  border-radius: 10px;
  font-size: 13.5px;
  font-weight: 500;
  z-index: 9999;
  box-shadow: var(--shadow-lg);
  transition: opacity 0.25s, transform 0.25s;
  pointer-events: none;
}
.toast.error { background: var(--danger); }
.toast.hidden { opacity: 0; transform: translateX(-50%) translateY(8px); }

/* ---------- Theme toggle ---------- */

.foot-actions { display: flex; align-items: center; gap: 8px; }
.theme-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 31px;
  height: 31px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--muted);
  cursor: pointer;
  flex-shrink: 0;
  transition: color 0.12s, background 0.12s;
}
.theme-btn:hover { color: var(--text); background: var(--surface-2); }

/* ---------- Responsive ---------- */

.mobile-nav {
  display: none;
  position: sticky;
  top: 0;
  z-index: 50;
  background: var(--nav-glass);
  backdrop-filter: blur(8px);
  border-bottom: 1px solid var(--border);
  padding: 10px 12px;
  gap: 8px;
  align-items: center;
}
.mobile-nav-items {
  display: flex;
  gap: 4px;
  flex: 1;
  overflow-x: auto;
}
.mobile-nav .nav-item { flex: 0 0 auto; width: auto; justify-content: center; padding: 8px 14px; }
.mobile-nav .nav-item span.mnav-label { font-size: 12.5px; }

@media (max-width: 1180px) {
  .stat-grid { grid-template-columns: repeat(2, 1fr); }
}

@media (max-width: 900px) {
  .shell { flex-direction: column; }
  .sidebar { display: none; }
  .main { padding: 24px 18px 48px; }
  .stat-grid { grid-template-columns: repeat(2, 1fr); }
  .mobile-nav { display: flex; }
}

@media (max-width: 560px) {
  .row { flex-direction: column; align-items: stretch; }
  .row > label { min-width: auto; }
  .stat-value { font-size: 22px; }
  .chart-col, .chart-axis-item { min-width: 12px; }
  /* Prevent iOS Safari auto-zoom on focus (requires >=16px). */
  input[type="text"], input[type="number"], input[type="password"] { font-size: 16px; }
}
</style>
</head>
<body>

<!-- Login -->
<div id="loginPage" class="login-wrap">
  <div class="login-card">
    <div class="login-mark">Q</div>
    <div class="login-title">Qoder-2API</div>
    <div class="login-subtitle">管理控制台 · 请输入管理密码</div>
    <input type="password" id="loginPassword" class="login-input" placeholder="管理密码" autofocus autocomplete="current-password">
    <button class="login-btn" type="button" onclick="doLogin()">登 录</button>
    <div id="loginError" class="login-error"></div>
  </div>
</div>

<!-- Dashboard -->
<div id="dashboard" class="hidden">
<div class="shell">

  <!-- Desktop sidebar -->
  <aside class="sidebar">
    <div class="brand">
      <div class="brand-mark">Q</div>
      <div>
        <div class="brand-name">Qoder-2API</div>
        <div class="brand-sub">管理控制台</div>
      </div>
    </div>
    <nav class="nav">
      <div class="nav-label">控制台</div>
      <button class="nav-item active" data-page="stats" onclick="switchPage('stats')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M3 13V8"/><path d="M7 13V3"/><path d="M11 13v-3"/><path d="M15 13H1"/></svg>
        <span>统计</span>
      </button>
      <button class="nav-item" data-page="keys" onclick="switchPage('keys')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="10" r="3.5"/><path d="M8.8 7.2 14 2"/><path d="M11 4.5l2 2"/><path d="M8.2 6.8 9.8 8.4"/></svg>
        <span>密钥</span>
      </button>
      <button class="nav-item" data-page="token" onclick="switchPage('token')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6.5V3.5h10v3"/><path d="M3.5 6.5h9l.5 7h-10l.5-7z"/><path d="M10.5 6.5v-2a2.5 2.5 0 0 0-5 0v2"/></svg>
        <span>令牌</span>
      </button>
      <button class="nav-item" data-page="models" onclick="switchPage('models')">
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="2.5" y="2.5" width="4.5" height="4.5" rx="1"/><rect x="9" y="2.5" width="4.5" height="4.5" rx="1"/><rect x="2.5" y="9" width="4.5" height="4.5" rx="1"/><rect x="9" y="9" width="4.5" height="4.5" rx="1"/></svg>
        <span>模型</span>
      </button>
      <button class="nav-item" data-page="config" onclick="switchPage('config')">
        <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
        <span>设置</span>
      </button>
    </nav>
    <div class="sidebar-foot">
      <span class="status"><span class="status-dot"></span>运行中</span>
      <div class="foot-actions">
        <button class="theme-btn" type="button" onclick="toggleTheme()" title="切换深浅模式" aria-label="切换深浅模式">
          <svg class="icon-moon" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
          <svg class="icon-sun hidden" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/></svg>
        </button>
        <button class="logout-btn" type="button" onclick="doLogout()">
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M10 2.5h2.5A1.5 1.5 0 0 1 14 4v8a1.5 1.5 0 0 1-1.5 1.5H10"/><path d="M6.5 5.5 9 8l-2.5 2.5"/><path d="M9 8H2"/></svg>
          退出
        </button>
      </div>
    </div>
  </aside>

  <!-- Mobile nav -->
  <div class="mobile-nav">
    <div class="mobile-nav-items" id="mobileNavItems"></div>
    <button class="theme-btn" type="button" onclick="toggleTheme()" title="切换深浅模式" aria-label="切换深浅模式">
      <svg class="icon-moon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
      <svg class="icon-sun hidden" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/></svg>
    </button>
  </div>

  <main class="main">
  <div class="main-inner">

    <!-- Stats -->
    <section id="page-stats" class="page">
      <div class="page-head">
        <div class="page-title">请求统计</div>
        <div class="page-desc">统计自服务启动以来记录的请求，每 30 秒持久化一次</div>
      </div>

      <div class="stat-grid">
        <div class="stat-card">
          <div class="stat-label"><span class="stat-dot total"></span>总请求</div>
          <div class="stat-value num" id="statTotal">0</div>
          <div class="stat-sub num" id="statReqSub">成功率 —</div>
        </div>
        <div class="stat-card">
          <div class="stat-label"><span class="stat-dot success"></span>Token 总数</div>
          <div class="stat-value num" id="statTokens">0</div>
          <div class="stat-sub num" id="statCredits">Credits 消耗 0</div>
        </div>
        <div class="stat-card">
          <div class="stat-label"><span class="stat-dot rate"></span>总输入</div>
          <div class="stat-value num" id="statPrompt">0</div>
          <div class="stat-sub num" id="statCached">缓存命中 0</div>
        </div>
        <div class="stat-card">
          <div class="stat-label"><span class="stat-dot total"></span>总输出</div>
          <div class="stat-value num" id="statCompletion">0</div>
          <div class="stat-sub num" id="statAvgOut">平均 0 / 次</div>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <div class="card-title">近 24 小时请求量</div>
          <div class="chart-legend">
            <span class="legend-item"><span class="legend-dot ok"></span>成功</span>
            <span class="legend-item"><span class="legend-dot fail"></span>失败</span>
            <span class="chart-peak num" id="chartPeak"></span>
          </div>
        </div>
        <div class="card-body">
          <div class="chart" id="chart"></div>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <div class="card-title">按模型统计</div>
        </div>
        <div class="card-body flush">
          <table>
            <thead>
              <tr><th>模型</th><th class="num">请求</th><th class="num">成功</th><th class="num">失败</th><th class="num" style="width:200px">成功率</th></tr>
            </thead>
            <tbody id="statsTable"></tbody>
          </table>
        </div>
      </div>
    </section>

    <!-- Keys -->
    <section id="page-keys" class="page hidden">
      <div class="page-head">
        <div class="page-title">API 密钥</div>
        <div class="page-desc">客户端调用 /v1/chat/completions 时使用的密钥</div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">创建密钥</div></div>
        <div class="card-body">
          <div class="row">
            <label>密钥</label>
            <div class="field"><input type="text" id="newKey" placeholder="留空则自动生成" autocomplete="off"></div>
          </div>
          <div class="row">
            <label>备注</label>
            <div class="field"><input type="text" id="newNote" placeholder="可选描述" autocomplete="off"></div>
          </div>
          <div class="btn-row right">
            <button class="btn secondary" type="button" onclick="addKey(true)">随机生成</button>
            <button class="btn" type="button" onclick="addKey(false)">创建</button>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">密钥列表</div></div>
        <div class="card-body flush">
          <table>
            <thead><tr><th>密钥</th><th>备注</th><th>创建时间</th><th class="num" style="width:170px">操作</th></tr></thead>
            <tbody id="keysTable"></tbody>
          </table>
        </div>
      </div>
    </section>

    <!-- Token -->
    <section id="page-token" class="page hidden">
      <div class="page-head">
        <div class="page-title">Qoder 令牌</div>
        <div class="page-desc">Qoder 个人访问令牌，API 桥接运行必须配置</div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">访问令牌</div></div>
        <div class="card-body">
          <p class="hint">令牌变更后，桥接会话将在下一次请求时自动重建。</p>
          <div class="row">
            <label>当前令牌</label>
            <div class="field"><input type="text" id="patDisplay" readonly></div>
          </div>
          <div class="row">
            <label>新令牌</label>
            <div class="field"><input type="text" id="patInput" placeholder="pt-..." autocomplete="off"></div>
          </div>
          <div class="btn-row right">
            <button class="btn" type="button" onclick="savePAT()">保存</button>
          </div>
        </div>
      </div>
    </section>

    <!-- Models -->
    <section id="page-models" class="page hidden">
      <div class="page-head">
        <div class="page-title">可用模型</div>
        <div class="page-desc">点击「复制」获取模型名称用于 API 调用</div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">模型列表</div></div>
        <div class="card-body">
          <div id="modelsList" class="model-grid"></div>
        </div>
      </div>
    </section>

    <!-- Settings -->
    <section id="page-config" class="page hidden">
      <div class="page-head">
        <div class="page-title">设置</div>
        <div class="page-desc">服务器监听配置与管理密码</div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">服务器配置</div></div>
        <div class="card-body">
          <p class="hint">配置监听地址和端口，修改后需要重启服务才能生效。</p>
          <div class="row">
            <label>主机</label>
            <div class="field"><input type="text" id="cfgHost" placeholder="0.0.0.0" autocomplete="off"></div>
          </div>
          <div class="row">
            <label>端口</label>
            <div class="field"><input type="number" id="cfgPort" placeholder="10081"></div>
          </div>
          <div id="configHint" class="hint-warn hidden"></div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">超时配置</div></div>
        <div class="card-body">
          <p class="hint">Chat 响应超时（秒）：等待上游开始响应（返回响应头）的最长时间；流空闲超时（秒）：流式响应中持续无数据的最长等待，只要数据持续到达就不会中断。保存后立即生效，无需重启。</p>
          <div class="row">
            <label>响应超时</label>
            <div class="field"><input type="number" id="cfgChatTimeout" placeholder="120" min="1" max="3600"></div>
          </div>
          <div class="row">
            <label>空闲超时</label>
            <div class="field"><input type="number" id="cfgIdleTimeout" placeholder="300" min="1" max="3600"></div>
          </div>
          <div class="btn-row right">
            <button class="btn" type="button" onclick="saveConfig()">保存</button>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><div class="card-title">修改密码</div></div>
        <div class="card-body">
          <p class="hint">修改管理控制台的登录密码。</p>
          <div class="row">
            <label>当前密码</label>
            <div class="field"><input type="password" id="curPwd" placeholder="当前密码" autocomplete="current-password"></div>
          </div>
          <div class="row">
            <label>新密码</label>
            <div class="field"><input type="password" id="newPwd" placeholder="新密码" autocomplete="new-password"></div>
          </div>
          <div class="btn-row right">
            <button class="btn" type="button" onclick="savePassword()">修改密码</button>
          </div>
        </div>
      </div>
    </section>

  </div>
  </main>
</div>
</div>

<div id="toast" class="toast hidden"></div>

<script>
/* ---------- Helpers ---------- */

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

function showToast(msg, type) {
  var t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'toast ' + (type || '');
  clearTimeout(showToast._timer);
  showToast._timer = setTimeout(function() { t.className = 'toast hidden'; }, 2800);
}

function escapeHtml(s) {
  var d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML.replace(/'/g, '&#39;');
}

function fmt(n) {
  return Number(n || 0).toLocaleString('zh-CN');
}

function pct(rate) {
  if (rate === null || rate === undefined || isNaN(rate)) return '—';
  return (rate * 100).toFixed(1) + '%';
}

function copyText(text) {
  navigator.clipboard.writeText(text).then(function() { showToast('已复制到剪贴板'); });
}

/* ---------- Auth ---------- */

function checkAuth() {
  api('/auth').then(function(data) {
    if (data.authenticated) { showDashboard(); }
    else { showLogin(); }
  }).catch(function() { showLogin(); });
}

function showLogin() {
  loggedIn = false;
  document.getElementById('loginPage').classList.remove('hidden');
  document.getElementById('dashboard').classList.add('hidden');
}

function showDashboard() {
  loggedIn = true;
  document.getElementById('loginPage').classList.add('hidden');
  document.getElementById('dashboard').classList.remove('hidden');
  buildMobileNav();
  switchPage('stats');
}

function doLogin() {
  var pwd = document.getElementById('loginPassword').value;
  if (!pwd) return;
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

/* ---------- Navigation ---------- */

var NAV_ITEMS = [
  ['stats', '统计', 'M3 13V8M7 13V3M11 13v-3M15 13H1'],
  ['keys', '密钥', 'M8.8 7.2 14 2M11 4.5l2 2M8.2 6.8 9.8 8.4'],
  ['token', '令牌', 'M3 6.5V3.5h10v3M3.5 6.5h9l.5 7h-10l.5-7z'],
  ['models', '模型', ''],
  ['config', '设置', '']
];
var PAGES = ['stats', 'keys', 'token', 'models', 'config'];

function buildMobileNav() {
  var nav = document.getElementById('mobileNavItems');
  nav.innerHTML = '';
  NAV_ITEMS.forEach(function(item) {
    var b = document.createElement('button');
    b.className = 'nav-item';
    b.setAttribute('data-page', item[0]);
    b.setAttribute('onclick', "switchPage('" + item[0] + "')");
    b.innerHTML = '<span class="mnav-label">' + item[1] + '</span>';
    nav.appendChild(b);
  });
}

function switchPage(name) {
  currentPage = name;
  PAGES.forEach(function(p) {
    document.getElementById('page-' + p).classList.add('hidden');
  });
  document.getElementById('page-' + name).classList.remove('hidden');
  document.querySelectorAll('.nav-item').forEach(function(el) {
    el.classList.toggle('active', el.getAttribute('data-page') === name);
  });
  if (name === 'stats') loadStats();
  if (name === 'keys') loadKeys();
  if (name === 'token') loadPAT();
  if (name === 'models') loadModels();
  if (name === 'config') loadConfig();
}

var currentPage = 'stats';
var loggedIn = false;
setInterval(function() {
  if (loggedIn && currentPage === 'stats') loadStats();
}, 15000);

/* ---------- Theme ---------- */

function themeDark() {
  var saved = null;
  try { saved = localStorage.getItem('qoder2api-theme'); } catch (e) {}
  if (saved === 'light' || saved === 'dark') return saved === 'dark';
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
}

function applyTheme(dark) {
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
  try { localStorage.setItem('qoder2api-theme', dark ? 'dark' : 'light'); } catch (e) {}
  document.querySelectorAll('.icon-moon').forEach(function(el) { el.classList.toggle('hidden', dark); });
  document.querySelectorAll('.icon-sun').forEach(function(el) { el.classList.toggle('hidden', !dark); });
}

function toggleTheme() {
  applyTheme(!themeDark());
}

applyTheme(themeDark());

/* ---------- Stats ---------- */

function loadStats() {
  api('/stats').then(function(d) {
    var tokens = (d.prompt_tokens || 0) + (d.completion_tokens || 0);
    document.getElementById('statTotal').textContent = fmt(d.total);
    document.getElementById('statReqSub').textContent = '成功率 ' + pct(d.success_rate);
    document.getElementById('statTokens').textContent = fmt(tokens);
    document.getElementById('statCredits').textContent = 'Credits 消耗 ' + fmtCredits(d.credits);
    document.getElementById('statPrompt').textContent = fmt(d.prompt_tokens);
    document.getElementById('statCached').textContent = '缓存命中 ' + fmt(d.cached_tokens);
    document.getElementById('statCompletion').textContent = fmt(d.completion_tokens);
    var avg = d.total > 0 ? Math.round((d.completion_tokens || 0) / d.total) : 0;
    document.getElementById('statAvgOut').textContent = '平均 ' + fmt(avg) + ' / 次';
    renderChart(d.hourly || []);
    renderStatsTable(d.by_model || []);
  }).catch(function(e) { showToast(e.message, 'error'); });
}

function fmtCredits(v) {
  var n = Number(v || 0);
  if (n === 0) return '0';
  if (n >= 1000) return n.toLocaleString('zh-CN', {maximumFractionDigits: 2});
  return n.toPrecision(6).replace(/\.?0+$/, '');
}

function renderChart(hourly) {
  var max = 0;
  hourly.forEach(function(h) { if (h.total > max) max = h.total; });
  document.getElementById('chartPeak').textContent = max > 0 ? '峰值 ' + fmt(max) : '暂无数据';

  var chart = document.getElementById('chart');
  chart.innerHTML = '';

  var plot = document.createElement('div');
  plot.className = 'chart-plot';
  var axis = document.createElement('div');
  axis.className = 'chart-axis';

  hourly.forEach(function(h, i) {
    var tip = h.label + ' · 总 ' + h.total + ' · 成功 ' + h.success + ' · 失败 ' + h.failed;

    var col = document.createElement('div');
    col.className = 'chart-col';
    col.title = tip;

    var bars = document.createElement('div');
    bars.className = 'chart-bars';
    if (h.total > 0 && max > 0) {
      var okH = Math.round(h.success / max * 100);
      var failH = Math.round(h.failed / max * 100);
      if (okH > 0) {
        var okBar = document.createElement('div');
        okBar.className = 'bar ok';
        okBar.style.height = okH + '%';
        bars.appendChild(okBar);
      }
      if (failH > 0) {
        var failBar = document.createElement('div');
        failBar.className = 'bar fail';
        failBar.style.height = failH + '%';
        bars.appendChild(failBar);
      }
    }
    col.appendChild(bars);
    plot.appendChild(col);

    var lab = document.createElement('div');
    lab.className = 'chart-axis-item';
    lab.textContent = (i % 4 === 0) ? h.label : '';
    lab.title = tip;
    axis.appendChild(lab);
  });

  chart.appendChild(plot);
  chart.appendChild(axis);
}

function renderStatsTable(rows) {
  var tb = document.getElementById('statsTable');
  tb.innerHTML = '';
  if (!rows || rows.length === 0) {
    tb.innerHTML = '<tr><td colspan="5"><div class="empty-row"><div class="empty-title">暂无统计数据</div>请求处理完成后会自动记录</div></td></tr>';
    return;
  }
  rows.forEach(function(r) {
    var tr = document.createElement('tr');
    var rate = r.success_rate;
    var cls = 'rate-fill';
    if (rate < 0.5) cls += ' bad';
    else if (rate < 0.9) cls += ' warn';
    tr.innerHTML =
      '<td><span class="mono">' + escapeHtml(r.model) + '</span></td>' +
      '<td class="num">' + fmt(r.total) + '</td>' +
      '<td class="num" style="color:var(--success)">' + fmt(r.success) + '</td>' +
      '<td class="num" style="color:var(--danger)">' + fmt(r.failed) + '</td>' +
      '<td><div class="rate-cell"><div class="rate-bar"><div class="' + cls + '" style="width:' + (rate * 100) + '%"></div></div>' +
      '<span class="rate-text num">' + pct(rate) + '</span></div></td>';
    tb.appendChild(tr);
  });
}

/* ---------- API Keys ---------- */

function loadKeys() {
  api('/keys').then(function(data) {
    var tb = document.getElementById('keysTable');
    tb.innerHTML = '';
    if (!data.keys || data.keys.length === 0) {
      tb.innerHTML = '<tr><td colspan="4"><div class="empty-row"><div class="empty-title">暂无密钥</div>在上方创建第一个密钥</div></td></tr>';
      return;
    }
    data.keys.forEach(function(k) {
      var tr = document.createElement('tr');
      var dt = new Date(k.created_at * 1000).toLocaleDateString('zh-CN');
      tr.innerHTML =
        '<td><span class="mono">' + escapeHtml(k.key) + '</span></td>' +
        '<td>' + (k.note ? escapeHtml(k.note) : '<span class="muted">—</span>') + '</td>' +
        '<td class="muted">' + dt + '</td>' +
        '<td><div class="btn-row" style="justify-content:flex-end"></div></td>';
      var btnRow = tr.querySelector('.btn-row');
      var copyBtn = document.createElement('button');
      copyBtn.className = 'btn secondary sm';
      copyBtn.type = 'button';
      copyBtn.textContent = '复制';
      copyBtn.addEventListener('click', function() { copyText(k.key); });
      var delBtn = document.createElement('button');
      delBtn.className = 'btn danger sm';
      delBtn.type = 'button';
      delBtn.textContent = '删除';
      delBtn.addEventListener('click', function() { deleteKey(k.id); });
      btnRow.appendChild(copyBtn);
      btnRow.appendChild(delBtn);
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
      showToast('密钥已创建');
      loadKeys();
    })
    .catch(function(e) { showToast(e.message, 'error'); });
}

function deleteKey(id) {
  if (!confirm('确定删除此密钥？')) return;
  api('/keys?id=' + encodeURIComponent(id), { method: 'DELETE' })
    .then(function() { showToast('密钥已删除'); loadKeys(); })
    .catch(function(e) { showToast(e.message, 'error'); });
}

/* ---------- PAT ---------- */

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
    .then(function() { showToast('令牌已保存'); loadPAT(); })
    .catch(function(e) { showToast(e.message, 'error'); });
}

/* ---------- Models ---------- */

function loadModels() {
  api('/models').then(function(data) {
    var g = document.getElementById('modelsList');
    g.innerHTML = '';
    if (!data.models || data.models.length === 0) {
      g.innerHTML = '<p class="hint" style="margin:0">暂无可用模型</p>';
      return;
    }
    data.models.forEach(function(m) {
      var d = document.createElement('div');
      d.className = 'model-item';
      d.innerHTML = '<span class="mono">' + escapeHtml(m) + '</span>';
      var copyBtn = document.createElement('button');
      copyBtn.className = 'btn secondary sm';
      copyBtn.type = 'button';
      copyBtn.textContent = '复制';
      copyBtn.addEventListener('click', function() { copyText(m); });
      d.appendChild(copyBtn);
      g.appendChild(d);
    });
  }).catch(function(e) { showToast(e.message, 'error'); });
}

/* ---------- Config ---------- */

function loadConfig() {
  api('/config').then(function(data) {
    document.getElementById('cfgHost').value = data.host || '';
    document.getElementById('cfgPort').value = data.port || '';
    document.getElementById('cfgChatTimeout').value = data.chat_timeout_seconds || '';
    document.getElementById('cfgIdleTimeout').value = data.idle_timeout_seconds || '';
    document.getElementById('configHint').classList.add('hidden');
  }).catch(function(e) { showToast(e.message, 'error'); });
}

function saveConfig() {
  var host = document.getElementById('cfgHost').value.trim();
  var port = parseInt(document.getElementById('cfgPort').value, 10);
  if (!host) host = '0.0.0.0';
  if (!port || port < 1 || port > 65535) { showToast('端口无效', 'error'); return; }
  var body = { host: host, port: port };
  var ct = parseInt(document.getElementById('cfgChatTimeout').value, 10);
  var it = parseInt(document.getElementById('cfgIdleTimeout').value, 10);
  if (document.getElementById('cfgChatTimeout').value.trim() !== '') {
    if (!ct || ct < 1 || ct > 3600) { showToast('Chat 响应超时需在 1-3600 秒之间', 'error'); return; }
    body.chat_timeout_seconds = ct;
  }
  if (document.getElementById('cfgIdleTimeout').value.trim() !== '') {
    if (!it || it < 1 || it > 3600) { showToast('流空闲超时需在 1-3600 秒之间', 'error'); return; }
    body.idle_timeout_seconds = it;
  }
  api('/config', { method: 'POST', body: JSON.stringify(body) })
    .then(function(data) {
      showToast('配置已保存（超时配置即时生效）');
      var h = document.getElementById('configHint');
      h.textContent = data.restart || '修改主机或端口后需要重启服务才能生效。';
      h.classList.remove('hidden');
      loadConfig();
    })
    .catch(function(e) { showToast(e.message, 'error'); });
}

/* ---------- Password ---------- */

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

/* ---------- Init ---------- */

checkAuth();
</script>
</body>
</html>`
