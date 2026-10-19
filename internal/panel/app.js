// app.js 面板前端逻辑（无框架、无外部依赖，同源脚本以满足严格 CSP）。
'use strict';

const state = {
  key: localStorage.getItem('loomy2api_key') || '',
  view: 'accounts',
  overview: null,
  accounts: [],
  models: [],
  config: null,
  timer: null,
};

// ---------------------------------------------------------------- 基础工具

function $(sel, root) { return (root || document).querySelector(sel); }
function $$(sel, root) { return Array.from((root || document).querySelectorAll(sel)); }

function el(tag, attrs, children) {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === null || v === undefined || v === false) continue;
      if (k === 'class') node.className = v;
      else if (k === 'text') node.textContent = v;
      else if (k === 'html') node.innerHTML = v;
      else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
      else node.setAttribute(k, v);
    }
  }
  for (const c of [].concat(children || [])) {
    if (c === null || c === undefined || c === false) continue;
    node.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return node;
}

function toast(msg, kind) {
  const t = el('div', { class: 'toast ' + (kind ? 'toast-' + kind : ''), text: msg });
  document.body.appendChild(t);
  setTimeout(() => t.remove(), kind === 'bad' ? 6000 : 3200);
}

async function api(path, opts) {
  const options = Object.assign({ headers: {} }, opts || {});
  if (state.key) options.headers['Authorization'] = 'Bearer ' + state.key;
  if (options.body && typeof options.body !== 'string') {
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(options.body);
  }
  const res = await fetch(path, options);
  if (res.status === 401) {
    showGate(true);
    throw new Error('认证失败');
  }
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (e) { data = { raw: text }; }
  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || ('HTTP ' + res.status);
    throw new Error(typeof msg === 'string' ? msg : JSON.stringify(msg));
  }
  return data;
}

function fmtNum(n) {
  if (n === null || n === undefined) return '—';
  if (typeof n !== 'number') return String(n);
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (Math.abs(n) >= 1e4) return (n / 1e3).toFixed(1) + 'k';
  return n.toLocaleString('zh-CN');
}

function fmtDuration(sec) {
  if (sec === null || sec === undefined || sec <= 0) return '—';
  if (sec < 60) return Math.round(sec) + 's';
  if (sec < 3600) return Math.round(sec / 60) + 'm';
  if (sec < 86400) return (sec / 3600).toFixed(1) + 'h';
  return (sec / 86400).toFixed(1) + 'd';
}

function fmtMs(ms) {
  if (!ms && ms !== 0) return '—';
  if (ms < 1000) return ms + 'ms';
  return (ms / 1000).toFixed(2) + 's';
}

function fmtTime(ts) {
  if (!ts) return '—';
  const d = new Date(ts * 1000);
  const pad = (n) => String(n).padStart(2, '0');
  return pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
}

// ---------------------------------------------------------------- 登录门

function showGate(show) {
  $('#login-gate').classList.toggle('hidden', !show);
  if (show) $('#gate-key').focus();
}

$('#gate-submit').addEventListener('click', submitGate);
$('#gate-key').addEventListener('keydown', (e) => { if (e.key === 'Enter') submitGate(); });

function submitGate() {
  const key = $('#gate-key').value.trim();
  if (!key) return;
  state.key = key;
  localStorage.setItem('loomy2api_key', key);
  $('#gate-error').textContent = '';
  refreshAll().then(() => showGate(false)).catch((e) => {
    $('#gate-error').textContent = e.message || '密钥不正确';
    state.key = '';
    localStorage.removeItem('loomy2api_key');
  });
}

// ---------------------------------------------------------------- 主题

function applyTheme(theme) {
  document.documentElement.setAttribute('data-theme', theme);
  localStorage.setItem('loomy2api_theme', theme);
}

(function initTheme() {
  const saved = localStorage.getItem('loomy2api_theme');
  if (saved) { applyTheme(saved); return; }
  const preferLight = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches;
  applyTheme(preferLight ? 'light' : 'dark');
})();

$('#btn-theme').addEventListener('click', () => {
  const cur = document.documentElement.getAttribute('data-theme');
  applyTheme(cur === 'dark' ? 'light' : 'dark');
});

// ---------------------------------------------------------------- 导航

$('#tabs').addEventListener('click', (e) => {
  const btn = e.target.closest('.tab');
  if (!btn) return;
  switchView(btn.dataset.view);
});

function switchView(view) {
  state.view = view;
  $$('.tab').forEach((b) => b.classList.toggle('active', b.dataset.view === view));
  $$('.view').forEach((v) => v.classList.toggle('active', v.id === 'view-' + view));
  if (view === 'usage') loadUsage();
  if (view === 'models') loadModels();
  if (view === 'logs') { loadRequests(); loadServiceLogs(); }
  if (view === 'config') loadConfig();
}

// ---------------------------------------------------------------- 刷新

async function refreshAll() {
  const [overview, accounts] = await Promise.all([
    api('/panel/api/overview'),
    api('/panel/api/accounts'),
  ]);
  state.overview = overview;
  state.accounts = accounts.accounts || [];
  renderHealth(overview);
  renderStatCards(overview);
  renderAccounts(state.accounts);
}

function renderHealth(ov) {
  const pill = $('#health');
  const healthy = ov.healthy || 0;
  const total = ov.total || 0;
  pill.textContent = total === 0 ? '无账号' : `可用 ${healthy}/${total}`;
  pill.className = 'pill ' + (healthy > 0 ? 'pill-ok' : 'pill-bad');
}

function renderStatCards(ov) {
  const u = (ov.usage && (ov.usage['24h'] || ov.usage.today)) || {};
  const cards = [
    { label: '账号', value: `${ov.healthy || 0}/${ov.total || 0}`, sub: '可用/总计' },
    { label: '可用积分', value: fmtNum(ov.available_points), sub: '' },
    { label: '24h 请求', value: fmtNum(u.total), sub: u.fail ? `失败 ${u.fail}` : '' },
    { label: '24h token', value: fmtNum((u.prompt_tokens || 0) + (u.completion_tokens || 0)), sub: 'prompt+completion' },
    { label: '24h 积分', value: fmtNum(u.points), sub: '' },
    { label: '在途', value: fmtNum(ov.inflight), sub: '并发请求' },
  ];
  const wrap = $('#stat-cards');
  wrap.replaceChildren(...cards.map((c) => el('div', { class: 'card' }, [
    el('div', { class: 'label', text: c.label }),
    el('div', { class: 'value' }, [
      String(c.value),
      c.sub ? el('small', { text: c.sub }) : null,
    ]),
  ])));
}

// ---------------------------------------------------------------- 账号表

function accountStatus(a) {
  if (!a.enabled) return { text: '已禁用', cls: 'tag-muted' };
  if (a.in_breaker) return { text: '熔断 ' + fmtDuration(a.breaker_seconds_left), cls: 'tag-bad' };
  if (a.in_cooldown) {
    const kind = a.cooldown_kind === 'hard' ? '额度冷却' : '限流冷却';
    return { text: kind + ' ' + fmtDuration(a.cooldown_seconds_left), cls: 'tag-warn' };
  }
  if (!a.session_valid) return { text: '登录态失效', cls: 'tag-bad' };
  return { text: '可用', cls: 'tag-ok' };
}

function renderAccounts(accounts) {
  const tbody = $('#accounts-table tbody');
  if (!accounts.length) {
    tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '7', class: 'empty', text: '还没有账号，点右上「添加账号」开始' })]));
    return;
  }
  const maxPoints = Math.max(1, ...accounts.map((a) => Number(a.available || 0)));
  tbody.replaceChildren(...accounts.map((a) => {
    const st = accountStatus(a);
    const nameCell = el('div', {}, [
      el('div', { text: a.name }),
      el('div', { class: 'mono dim', text: a.loginid || (a.userid ? 'uid ' + a.userid : '') }),
    ]);
    const points = Number(a.available || 0);
    const pointsCell = el('div', {}, [
      el('div', { class: 'mono', text: fmtNum(a.available) }),
      el('div', { class: 'bar' }, [el('span', { style: 'width:' + Math.round(points / maxPoints * 100) + '%' })]),
    ]);
    const sessCell = el('div', {}, [
      el('div', { class: 'mono', text: a.session_days_left !== null && a.session_days_left !== undefined ? a.session_days_left.toFixed(1) + 'd' : '—' }),
      el('div', { class: 'mono dim', text: a.session || '' }),
    ]);
    const ops = el('div', { class: 'btn-row' }, [
      el('button', { class: 'btn btn-sm', text: '登录', onclick: () => act(a.name, 'login') }),
      el('button', { class: 'btn btn-sm', text: '刷额度', onclick: () => act(a.name, 'refresh') }),
      el('button', { class: 'btn btn-sm', text: '解冻', onclick: () => act(a.name, 'revive') }),
      a.enabled
        ? el('button', { class: 'btn btn-sm btn-danger', text: '禁用', onclick: () => act(a.name, 'disable') })
        : el('button', { class: 'btn btn-sm', text: '启用', onclick: () => act(a.name, 'enable') }),
      el('button', { class: 'btn btn-sm btn-danger', text: '移除', onclick: () => removeAccount(a.name) }),
    ]);
    return el('tr', {}, [
      el('td', {}, [nameCell]),
      el('td', {}, [el('span', { class: 'tag ' + st.cls, text: st.text })]),
      el('td', {}, [pointsCell]),
      el('td', {}, [sessCell]),
      el('td', { class: 'mono' }, [fmtNum(a.requests) + ' / ' + fmtNum(a.failures)]),
      el('td', { class: 'mono', text: String(a.inflight || 0) }),
      el('td', {}, [ops]),
    ]);
  }));
}

async function act(name, action) {
  try {
    const res = await api(`/panel/api/accounts/${encodeURIComponent(name)}/${action}`, { method: 'POST' });
    if (res && res.error) { toast(res.error, 'bad'); return; }
    if (res && res.ok === false && res.error) { toast(res.error, 'bad'); return; }
    toast(`${name} · ${action} 完成`, 'ok');
    await refreshAll();
  } catch (e) {
    toast(e.message, 'bad');
  }
}

async function removeAccount(name) {
  if (!confirm(`确认移除账号 ${name}？凭证文件也会删除。`)) return;
  try {
    await api(`/panel/api/accounts/${encodeURIComponent(name)}/remove`, { method: 'POST' });
    toast(`${name} 已移除`, 'ok');
    await refreshAll();
  } catch (e) {
    toast(e.message, 'bad');
  }
}

// 添加账号
$('#btn-add-toggle').addEventListener('click', () => {
  $('#add-form').classList.toggle('hidden');
});
$('#btn-add-cancel').addEventListener('click', () => {
  $('#add-form').classList.add('hidden');
});
$('#add-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = {
    name: (f.get('name') || '').trim(),
    loginid: (f.get('loginid') || '').trim(),
    password: f.get('password') || '',
    proxy: (f.get('proxy') || '').trim(),
    login: true,
  };
  try {
    const res = await api('/panel/api/accounts', { method: 'POST', body });
    if (res.error) { toast(res.error, 'bad'); return; }
    toast('账号已添加', 'ok');
    e.target.reset();
    $('#add-form').classList.add('hidden');
    await refreshAll();
  } catch (err) {
    toast(err.message, 'bad');
  }
});

$('#btn-refresh-all').addEventListener('click', async () => {
  const btn = $('#btn-refresh-all');
  btn.disabled = true;
  btn.textContent = '刷新中…';
  try {
    const res = await api('/panel/api/refresh_all', { method: 'POST' });
    toast(`已刷新 ${(res.results || []).length} 个账号`, 'ok');
    await refreshAll();
  } catch (e) {
    toast(e.message, 'bad');
  } finally {
    btn.disabled = false;
    btn.textContent = '全量刷新额度';
  }
});

$('#btn-login-all').addEventListener('click', async () => {
  if (!confirm('对全部有手机号+密码的账号重新登录？')) return;
  const btn = $('#btn-login-all');
  btn.disabled = true;
  btn.textContent = '登录中…';
  try {
    const res = await api('/panel/api/login_all', { method: 'POST' });
    const ok = (res.results || []).filter((r) => r.ok).length;
    toast(`重新登录：成功 ${ok} / ${(res.results || []).length}`, ok ? 'ok' : 'bad');
    await refreshAll();
  } catch (e) {
    toast(e.message, 'bad');
  } finally {
    btn.disabled = false;
    btn.textContent = '全量重新登录';
  }
});

$('#btn-refresh').addEventListener('click', () => {
  refreshAll().then(() => toast('已刷新', 'ok')).catch((e) => toast(e.message, 'bad'));
});

// ---------------------------------------------------------------- 用量

$('#usage-window').addEventListener('change', loadUsage);

async function loadUsage() {
  const win = $('#usage-window').value;
  const now = Math.floor(Date.now() / 1000);
  const spans = { today: 86400, '24h': 86400, '3d': 259200, '7d': 604800, '30d': 2592000 };
  const from = now - (spans[win] || 86400);
  try {
    const data = await api(`/panel/api/usage?from=${from}&hours=24`);
    renderUsage(data);
  } catch (e) {
    toast(e.message, 'bad');
  }
}

function renderUsage(data) {
  const w = data.window || {};
  const cards = [
    { label: '请求', value: fmtNum(w.total), sub: w.fail ? `失败 ${w.fail}` : '' },
    { label: 'prompt token', value: fmtNum(w.prompt_tokens) },
    { label: 'completion token', value: fmtNum(w.completion_tokens) },
    { label: '思考 token', value: fmtNum(w.reasoning_tokens) },
    { label: '积分', value: fmtNum(w.points) },
    { label: '平均延迟', value: fmtMs(w.avg_latency_ms) },
  ];
  $('#usage-cards').replaceChildren(...cards.map((c) => el('div', { class: 'card' }, [
    el('div', { class: 'label', text: c.label }),
    el('div', { class: 'value' }, [String(c.value), c.sub ? el('small', { text: c.sub }) : null]),
  ])));

  // 时序柱状图
  const hourly = data.hourly || [];
  const maxTok = Math.max(1, ...hourly.map((h) => h.tokens || 0));
  $('#hourly-chart').replaceChildren(...hourly.map((h) => el('div', {
    class: 'chart-bar',
    title: `${h.hour} · ${h.requests} 次 · ${h.tokens} token · ${h.points} 积分`,
    style: 'height:' + Math.max(2, (h.tokens || 0) / maxTok * 100) + '%',
  })));

  fillGroupTable('#usage-by-account', w.by_account);
  fillGroupTable('#usage-by-model', w.by_model);
}

function fillGroupTable(sel, groups) {
  const tbody = $(sel + ' tbody');
  const rows = Object.entries(groups || {}).sort((a, b) => b[1].requests - a[1].requests);
  if (!rows.length) {
    tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '4', class: 'empty', text: '暂无数据' })]));
    return;
  }
  tbody.replaceChildren(...rows.map(([key, b]) => el('tr', {}, [
    el('td', { class: 'mono', text: key }),
    el('td', { class: 'mono', text: fmtNum(b.requests) }),
    el('td', { class: 'mono', text: fmtNum((b.prompt_tokens || 0) + (b.completion_tokens || 0)) }),
    el('td', { class: 'mono', text: fmtNum(b.points) }),
  ])));
}

// ---------------------------------------------------------------- 模型

$('#model-filter').addEventListener('input', () => renderModels(state.models));

async function loadModels() {
  try {
    const data = await api('/panel/api/models');
    state.models = data.models || [];
    renderModels(state.models);
  } catch (e) {
    toast(e.message, 'bad');
  }
}

function renderModels(models) {
  const kw = $('#model-filter').value.trim().toLowerCase();
  const filtered = kw
    ? models.filter((m) => (m.id + ' ' + (m.name || '')).toLowerCase().includes(kw))
    : models;
  const tbody = $('#models-table tbody');
  if (!filtered.length) {
    tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '7', class: 'empty', text: '没有匹配的模型' })]));
    return;
  }
  tbody.replaceChildren(...filtered.map((m) => el('tr', {}, [
    el('td', { class: 'mono', text: m.id }),
    el('td', { text: m.name || '—' }),
    el('td', { class: 'mono', text: m.multiplier ? 'x' + m.multiplier.toFixed(1) : '—' }),
    el('td', { class: 'mono', text: m.context_length ? fmtNum(m.context_length) : '—' }),
    el('td', { class: 'mono', text: m.max_output_tokens ? fmtNum(m.max_output_tokens) : '—' }),
    el('td', { text: (m.capabilities || []).join(', ') || '—' }),
    el('td', { text: (m.reasoning_efforts || []).join(', ') || '—' }),
  ])));
}

// ---------------------------------------------------------------- 日志

$('#log-filter').addEventListener('input', debounce(loadRequests, 300));
$('#log-result').addEventListener('change', loadRequests);

function debounce(fn, ms) {
  let t = null;
  return function () {
    clearTimeout(t);
    t = setTimeout(fn, ms);
  };
}

async function loadRequests() {
  const q = encodeURIComponent($('#log-filter').value.trim());
  const result = $('#log-result').value;
  try {
    const data = await api(`/panel/api/requests?limit=200&q=${q}&result=${result}`);
    const rows = data.records || [];
    const tbody = $('#requests-table tbody');
    if (!rows.length) {
      tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '8', class: 'empty', text: '暂无请求记录' })]));
      return;
    }
    tbody.replaceChildren(...rows.map((r) => {
      const ok = r.status >= 200 && r.status < 300;
      return el('tr', {}, [
        el('td', { class: 'mono', text: fmtTime(r.t) }),
        el('td', {}, [el('span', { class: 'tag ' + (ok ? 'tag-ok' : 'tag-bad'), text: String(r.status) })]),
        el('td', { class: 'mono', text: r.model || '—' }),
        el('td', { class: 'mono', text: r.account || '—' }),
        el('td', { class: 'mono dim', text: (r.ip || '—') + (r.ua ? ' · ' + r.ua.slice(0, 24) : '') }),
        el('td', { class: 'mono', text: fmtMs(r.latency_ms) }),
        el('td', { class: 'mono', text: fmtNum((r.prompt || 0) + (r.completion || 0)) }),
        el('td', { class: 'mono', text: fmtNum(r.points) }),
      ]);
    }));
  } catch (e) {
    toast(e.message, 'bad');
  }
}

async function loadServiceLogs() {
  try {
    const data = await api('/panel/api/logs');
    const box = $('#service-logs');
    const lines = data.lines || [];
    box.textContent = lines.length ? lines.join('\n') : '（暂无日志）';
    if ($('#log-autoscroll').checked) box.scrollTop = box.scrollHeight;
  } catch (e) {
    /* 忽略：日志页不阻断使用 */
  }
}

// ---------------------------------------------------------------- 配置

async function loadConfig() {
  if (state.config) return;
  try {
    const data = await api('/panel/api/config');
    state.config = data.config || {};
    $('#config-path').textContent = data.path || '';
    renderConfig(state.config);
  } catch (e) {
    toast(e.message, 'bad');
  }
}

const CONFIG_FIELDS = [
  {
    title: '基础',
    fields: [
      { key: 'listen', label: '监听地址', type: 'text' },
      { key: 'default_model', label: '默认模型', type: 'text' },
      { key: 'upstream', label: '上游 base URL', type: 'text' },
      { key: 'account_base', label: '账号服务 URL', type: 'text' },
      { key: 'proxy', label: '全局代理', type: 'text' },
      { key: 'loomy_version', label: 'loomy-version 头', type: 'text' },
    ],
  },
  {
    title: '鉴权',
    fields: [
      { key: 'panel_password', label: '面板密码（留空=用 api_key）', type: 'password' },
    ],
  },
  {
    title: '账号池',
    fields: [
      { key: 'strategy', label: '选号策略 (weighted/round_robin/balance/lru)', type: 'text' },
      { key: 'pick_top_n', label: 'Top-N 短名单', type: 'number' },
      { key: 'max_inflight_per_account', label: '单账号在途上限 (0=不限)', type: 'number' },
      { key: 'balance_floor', label: '积分保底 (0=关闭)', type: 'number' },
      { key: 'sticky_ttl_seconds', label: '会话粘性 TTL (秒)', type: 'number' },
      { key: 'sticky_max_entries', label: '粘性表容量', type: 'number' },
    ],
  },
  {
    title: '冷却与熔断',
    fields: [
      { key: 'soft_rate_base_seconds', label: '软冷却基数 (秒)', type: 'number' },
      { key: 'soft_rate_max_seconds', label: '软冷却封顶 (秒)', type: 'number' },
      { key: 'not_found_cooldown_seconds', label: '404 冷却 (秒)', type: 'number' },
      { key: 'breaker_threshold', label: '熔断阈值 (连败次数)', type: 'number' },
      { key: 'breaker_cooldown_seconds', label: '熔断基数 (秒)', type: 'number' },
      { key: 'breaker_cooldown_max_seconds', label: '熔断封顶 (秒)', type: 'number' },
    ],
  },
  {
    title: '上游与请求',
    fields: [
      { key: 'timeout_seconds', label: '短 RPC 超时 (秒)', type: 'number' },
      { key: 'max_retries', label: '换号重试次数', type: 'number' },
      { key: 'keepalive_min_tokens', label: '长任务阈值 (max_tokens)', type: 'number' },
      { key: 'quota_refresh_minutes', label: '额度刷新间隔 (分)', type: 'number' },
      { key: 'session_renew_before_days', label: '提前续期天数', type: 'number' },
    ],
  },
  {
    title: '日志',
    fields: [
      { key: 'log_requests', label: '记录请求日志', type: 'bool' },
      { key: 'log_client_info', label: '记录调用来源 (IP/UA)', type: 'bool' },
      { key: 'usage_max_entries', label: '台账内存条数', type: 'number' },
      { key: 'strip_model_prefix', label: '自动补 imodel/ 前缀', type: 'bool' },
    ],
  },
];

function renderConfig(cfg) {
  const form = $('#config-form');
  form.replaceChildren(...CONFIG_FIELDS.map((group) => el('div', { class: 'config-group' }, [
    el('h3', { text: group.title }),
    ...group.fields.map((f) => {
      const value = cfg[f.key];
      if (f.type === 'bool') {
        const input = el('input', { type: 'checkbox', name: f.key });
        input.checked = !!value;
        return el('label', { class: 'checkbox' }, [input, f.label]);
      }
      let shown = value;
      if (f.type === 'password' && value) shown = '';
      const input = el('input', {
        name: f.key,
        type: f.type === 'number' ? 'number' : (f.type === 'password' ? 'password' : 'text'),
        value: shown === undefined || shown === null ? '' : String(shown),
      });
      if (f.type === 'password' && value) input.placeholder = String(value);
      return el('label', {}, [f.label, input]);
    }),
  ])));
}

$('#btn-config-save').addEventListener('click', async () => {
  const form = $('#config-form');
  const patch = {};
  for (const group of CONFIG_FIELDS) {
    for (const f of group.fields) {
      const input = form.querySelector(`[name="${f.key}"]`);
      if (!input) continue;
      if (f.type === 'bool') patch[f.key] = input.checked;
      else if (f.type === 'number') {
        const n = Number(input.value);
        if (input.value !== '' && !Number.isNaN(n)) patch[f.key] = n;
      } else {
        const v = input.value.trim();
        if (f.type === 'password' && v === '') continue; // 留空 = 不改
        patch[f.key] = v;
      }
    }
  }
  try {
    const res = await api('/panel/api/config', { method: 'POST', body: patch });
    if (res.error) { toast(res.error, 'bad'); return; }
    $('#config-note').textContent = res.message || '已保存';
    toast('配置已保存', 'ok');
    state.config = null;
    await refreshAll();
  } catch (e) {
    toast(e.message, 'bad');
  }
});

// ---------------------------------------------------------------- 轮询

function startPolling() {
  if (state.timer) clearInterval(state.timer);
  state.timer = setInterval(() => {
    if (document.hidden) return;
    if (state.view === 'accounts') {
      refreshAll().catch(() => {});
    } else if (state.view === 'logs') {
      loadServiceLogs();
    }
  }, 5000);
}

// ---------------------------------------------------------------- 启动

(async function boot() {
  try {
    await refreshAll();
    showGate(false);
  } catch (e) {
    showGate(true);
  }
  startPolling();
})();
