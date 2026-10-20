// app.js 面板前端逻辑（无框架、无外部依赖，同源脚本以满足 CSP script-src 'self'）。
'use strict';

const THEME_KEY = 'loomy2api.theme';
const state = {
  key: localStorage.getItem('loomy2api_key') || '',
  view: 'accounts',
  overview: null,
  accounts: [],
  models: [],
  cfg: null,
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

// Toast 挂在 #toasts，左侧色条区分结果（蓝本同款）。
function toast(msg, kind) {
  const cls = 'tst' + (kind === 'ok' ? ' ok' : kind === 'bad' ? ' err' : '');
  const t = el('div', { class: cls, text: msg });
  $('#toasts').appendChild(t);
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
    openKey();
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
  const p = (n) => String(n).padStart(2, '0');
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

// ---------------------------------------------------------------- 主题

// 三态循环：跟随系统 → 浅色 → 深色。判定与 theme.js 必须一致，否则刷新二次闪烁。
function effTheme() {
  const saved = localStorage.getItem(THEME_KEY) || 'auto';
  if (saved !== 'auto') return saved;
  return window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}
function applyTheme(saved) {
  localStorage.setItem(THEME_KEY, saved);
  document.documentElement.setAttribute('data-theme', effTheme());
}
$('#btnTheme').addEventListener('click', () => {
  const cur = localStorage.getItem(THEME_KEY) || 'auto';
  applyTheme(cur === 'auto' ? 'light' : cur === 'light' ? 'dark' : 'auto');
});

// ---------------------------------------------------------------- 密钥门

function openKey() {
  $('#keyVeil').classList.add('on');
  $('#keyInput').focus();
}
function closeKey() { $('#keyVeil').classList.remove('on'); }

function submitKey() {
  const key = $('#keyInput').value.trim();
  if (!key) return;
  state.key = key;
  localStorage.setItem('loomy2api_key', key);
  $('#keyErr').hidden = true;
  refreshAll().then(() => {
    closeKey();
    loadView(state.view);
  }).catch((e) => {
    $('#keyErr').textContent = e.message || '密钥不正确，请重试。';
    $('#keyErr').hidden = false;
    state.key = '';
    localStorage.removeItem('loomy2api_key');
  });
}
$('#btnKey').addEventListener('click', submitKey);
$('#keyInput').addEventListener('keydown', (e) => { if (e.key === 'Enter') submitKey(); });

$('#btnLogout').addEventListener('click', () => {
  state.key = '';
  localStorage.removeItem('loomy2api_key');
  $('#keyInput').value = '';
  openKey();
});

// ---------------------------------------------------------------- 导航

const TITLES = { accounts: '账号池', usage: '用量', models: '模型与档位', logs: '运行日志', config: '配置' };

$('.nav ul').addEventListener('click', (e) => {
  const a = e.target.closest('a[data-view]');
  if (!a) return;
  e.preventDefault();
  switchView(a.dataset.view);
  closeNav();
});
window.addEventListener('hashchange', () => {
  const v = location.hash.replace('#', '');
  if (TITLES[v]) switchView(v);
});

function switchView(view) {
  state.view = view;
  $$('.nav a[data-view]').forEach((a) => a.classList.toggle('on', a.dataset.view === view));
  $$('.view').forEach((s) => { s.hidden = s.id !== 'view-' + view; });
  $('#ttl').textContent = TITLES[view] || view;
  if (location.hash !== '#' + view) history.replaceState(null, '', '#' + view);
  loadView(view);
}

function loadView(view) {
  if (view === 'usage') loadUsage();
  if (view === 'models') loadModels();
  if (view === 'logs') { loadRequests(); loadServiceLogs(); loadErrors(); }
  if (view === 'config') loadConfig();
}

// 手机版导航抽屉
function closeNav() {
  $('#nav').classList.remove('open');
  $('#navScrim').classList.remove('on');
  $('#btnNav').setAttribute('aria-expanded', 'false');
  document.body.classList.remove('nav-open');
}
$('#btnNav').addEventListener('click', () => {
  const open = !$('#nav').classList.contains('open');
  $('#nav').classList.toggle('open', open);
  $('#navScrim').classList.toggle('on', open);
  $('#btnNav').setAttribute('aria-expanded', String(open));
  document.body.classList.toggle('nav-open', open);
});
$('#navScrim').addEventListener('click', closeNav);

// ---------------------------------------------------------------- 账号池

async function refreshAll() {
  const [overview, accounts] = await Promise.all([
    api('/panel/api/overview'),
    api('/panel/api/accounts'),
  ]);
  state.overview = overview;
  state.accounts = accounts.accounts || [];
  renderStats(overview);
  renderAccounts(state.accounts);
  renderSig(overview);
}

function renderSig(ov) {
  const healthy = ov.healthy || 0;
  const total = ov.total || 0;
  const pulse = $('#navPulse');
  pulse.className = 'pulse' + (total === 0 ? ' warn' : healthy === 0 ? ' bad' : '');
  $('#navState').textContent = total === 0 ? '无账号' : `可用 ${healthy}/${total}`;
  $('#navVer').textContent = 'v' + (ov.version || '-');
  $('#navUp').textContent = ov.upstream || '-';
  $('#subMeta').textContent = total === 0 ? '无账号' : `${healthy}/${total} 可用`;
  $('#accNote').textContent = total === 0 ? '还没有账号' : `${total} 个账号`;
}

function renderStats(ov) {
  const u = (ov.usage && (ov.usage['24h'] || ov.usage.today)) || {};
  $('#sTotal').textContent = fmtNum(ov.total);
  $('#sHealthy').textContent = fmtNum(ov.healthy);
  $('#sCooling').textContent = fmtNum(ov.cooling);
  $('#sDisabled').textContent = fmtNum(ov.disabled);
  $('#sPoints').textContent = fmtNum(ov.available_points);
  $('#sInflight').textContent = fmtNum(ov.inflight);
  $('#subMeta').textContent = `${ov.healthy || 0}/${ov.total || 0} 可用 · 24h ${fmtNum(u.total)} 请求`;
}

function accountStatus(a) {
  if (!a.enabled) return { text: '已禁用', cls: 'mute', row: 'off' };
  if (a.in_breaker) return { text: '熔断 ' + fmtDuration(a.breaker_seconds_left), cls: 'bad', row: 'off' };
  if (a.in_cooldown) {
    const kind = a.cooldown_kind === 'hard' ? '额度冷却' : '限流冷却';
    return { text: kind + ' ' + fmtDuration(a.cooldown_seconds_left), cls: 'warn', row: 'cool' };
  }
  if (!a.session_valid) return { text: '登录态失效', cls: 'bad', row: 'off' };
  return { text: '可用', cls: 'ok', row: '' };
}

function renderAccounts(accounts) {
  const tbody = $('#accBody');
  if (!accounts.length) {
    tbody.replaceChildren(el('tr', {}, [
      el('td', { colspan: '8', class: 'empty' }, [el('div', { class: 'big', text: '还没有账号' }), '点右上「+」添加']),
    ]));
    return;
  }
  const maxPts = Math.max(1, ...accounts.map((a) => Number(a.available || 0)));
  tbody.replaceChildren(...accounts.map((a) => {
    const st = accountStatus(a);
    const pts = Number(a.available || 0);
    const ops = el('div', { class: 'acts' }, [
      el('button', { class: 'xs', text: '登录', onclick: () => act(a.name, 'login') }),
      el('button', { class: 'xs', text: '刷额度', onclick: () => act(a.name, 'refresh') }),
      el('button', { class: 'xs', text: '解冻', onclick: () => act(a.name, 'revive') }),
      a.enabled
        ? el('button', { class: 'xs danger', text: '禁用', onclick: () => act(a.name, 'disable') })
        : el('button', { class: 'xs', text: '启用', onclick: () => act(a.name, 'enable') }),
      el('button', { class: 'xs danger', text: '移除', onclick: () => removeAccount(a.name) }),
    ]);
    return el('tr', { class: st.row }, [
      el('td', { class: 'mark' }, [el('i')]),
      el('td', { class: 'who' }, [
        el('div', { class: 'nm', text: a.name }),
        el('div', { class: 'id', text: a.loginid || (a.userid ? 'uid ' + a.userid : '—') }),
      ]),
      el('td', {}, [el('span', { class: 'tag ' + st.cls, text: st.text })]),
      el('td', { class: 'cred' }, [
        el('div', { class: 'n', text: fmtNum(a.available) }),
        el('div', { class: 'bar' }, [el('i', { style: 'width:' + Math.round(pts / maxPts * 100) + '%' })]),
      ]),
      el('td', { class: 'num' }, [
        a.session_days_left !== null && a.session_days_left !== undefined
          ? a.session_days_left.toFixed(1) + 'd'
          : '—',
      ]),
      el('td', { class: 'num', text: fmtNum(a.requests) + ' / ' + fmtNum(a.failures) }),
      el('td', { class: 'num', text: String(a.inflight || 0) }),
      el('td', { class: 'c-acts' }, [ops]),
    ]);
  }));
}

async function act(name, action) {
  try {
    await api(`/panel/api/accounts/${encodeURIComponent(name)}/${action}`, { method: 'POST' });
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

$('#btnRefreshAll').addEventListener('click', async () => {
  const btn = $('#btnRefreshAll');
  btn.disabled = true;
  btn.textContent = '刷新中…';
  try {
    const res = await api('/panel/api/refresh_all', { method: 'POST' });
    const bad = (res.results || []).filter((r) => !r.ok).length;
    toast(`已刷新 ${(res.results || []).length} 个账号` + (bad ? `，${bad} 个失败` : ''), bad ? 'bad' : 'ok');
    await refreshAll();
  } catch (e) {
    toast(e.message, 'bad');
  } finally {
    btn.disabled = false;
    btn.textContent = '全部刷新额度';
  }
});

$('#btnLoginAll').addEventListener('click', async () => {
  if (!confirm('对全部有手机号+密码的账号重新登录？')) return;
  const btn = $('#btnLoginAll');
  btn.disabled = true;
  btn.textContent = '登录中…';
  try {
    const res = await api('/panel/api/login_all', { method: 'POST' });
    const list = res.results || [];
    const ok = list.filter((r) => r.ok).length;
    toast(`重新登录：成功 ${ok} / ${list.length}`, ok ? 'ok' : 'bad');
    await refreshAll();
  } catch (e) {
    toast(e.message, 'bad');
  } finally {
    btn.disabled = false;
    btn.textContent = '全部重新登录';
  }
});

$('#btnRefresh').addEventListener('click', () => {
  refreshAll().then(() => loadView(state.view)).then(() => toast('已刷新', 'ok')).catch((e) => toast(e.message, 'bad'));
});

// 表单不提交（CSP 禁内联事件处理器，这里统一拦）
['#addForm', '#cfgForm'].forEach((sel) => {
  $(sel).addEventListener('submit', (e) => e.preventDefault());
});

// 添加账号（弹层）
$('#btnAdd').addEventListener('click', () => {
  $('#addErr').hidden = true;
  $('#addVeil').classList.add('on');
  $('#addForm').querySelector('[name=name]').focus();
});
$('#btnAddCancel').addEventListener('click', () => $('#addVeil').classList.remove('on'));
$('#btnAddSave').addEventListener('click', async () => {
  const f = new FormData($('#addForm'));
  const body = {
    name: (f.get('name') || '').trim(),
    loginid: (f.get('loginid') || '').trim(),
    password: f.get('password') || '',
    session: (f.get('session') || '').trim(),
    proxy: (f.get('proxy') || '').trim(),
    login: true,
  };
  try {
    await api('/panel/api/accounts', { method: 'POST', body });
    toast('账号已添加', 'ok');
    $('#addForm').reset();
    $('#addVeil').classList.remove('on');
    await refreshAll();
  } catch (err) {
    $('#addErr').textContent = err.message;
    $('#addErr').hidden = false;
  }
});

// ---------------------------------------------------------------- 用量

$('#usageWindow').addEventListener('change', loadUsage);

async function loadUsage() {
  const win = $('#usageWindow').value;
  const spans = { today: 86400, '24h': 86400, '3d': 259200, '7d': 604800, '30d': 2592000 };
  const from = Math.floor(Date.now() / 1000) - (spans[win] || 86400);
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
    { k: '请求', v: fmtNum(w.total), cls: '' },
    { k: 'prompt token', v: fmtNum(w.prompt_tokens), cls: '' },
    { k: 'completion token', v: fmtNum(w.completion_tokens), cls: '' },
    { k: '思考 token', v: fmtNum(w.reasoning_tokens), cls: '' },
    { k: '积分', v: fmtNum(w.points), cls: '' },
    { k: '平均延迟', v: fmtMs(w.avg_latency_ms), cls: '' },
  ];
  if (w.fail) cards[0].cls = 'bad';
  $('#usStats').replaceChildren(...cards.map((c) => el('div', { class: 'stat ' + c.cls }, [
    el('div', { class: 'v', text: String(c.v) }),
    el('div', { class: 'k', text: c.k }),
  ])));
  $('#usNote').textContent = w.fail ? `${fmtNum(w.total)} 请求 · ${fmtNum(w.fail)} 失败` : `${fmtNum(w.total)} 请求`;

  // token 全 0 时退化按请求数缩放，否则整段只有 2% 的空杠
  const hourly = data.hourly || [];
  const metric = (h) => (h.tokens || 0) || (h.requests || 0);
  const max = Math.max(1, ...hourly.map(metric));
  $('#usChart').replaceChildren(...hourly.map((h) => el('div', {
    class: 'b',
    style: 'height:' + Math.max(2, metric(h) / max * 100) + '%',
    title: `${h.hour} · ${h.requests} 次 · ${h.tokens} token · ${h.points} 积分`,
  })));

  fillGroup('#usByAccount', w.by_account);
  fillGroup('#usByModel', w.by_model);
}

function fillGroup(sel, groups) {
  const tbody = $(sel);
  const rows = Object.entries(groups || {}).sort((a, b) => b[1].requests - a[1].requests);
  if (!rows.length) {
    tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '5', class: 'empty', text: '暂无数据' })]));
    return;
  }
  tbody.replaceChildren(...rows.map(([key, b]) => el('tr', {}, [
    el('td', { class: 'num', text: key }),
    el('td', { class: 'num', text: fmtNum(b.requests) }),
    el('td', { class: 'num', text: fmtNum((b.prompt_tokens || 0) + (b.completion_tokens || 0)) }),
    el('td', { class: 'num', text: fmtNum(b.points) }),
    el('td', { class: 'num', text: b.fail ? fmtNum(b.fail) : '—' }),
  ])));
}

// ---------------------------------------------------------------- 模型

$('#modelFilter').addEventListener('input', () => renderModels(state.models));

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
  const kw = $('#modelFilter').value.trim().toLowerCase();
  const list = kw ? models.filter((m) => (m.id + ' ' + (m.name || '')).toLowerCase().includes(kw)) : models;
  const tbody = $('#mdBody');
  if (!list.length) {
    tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '7', class: 'empty', text: '没有匹配的模型' })]));
    return;
  }
  tbody.replaceChildren(...list.map((m) => el('tr', {}, [
    el('td', { class: 'num', text: m.id }),
    el('td', { text: m.name || '—' }),
    el('td', { class: 'num', text: m.multiplier ? 'x' + m.multiplier.toFixed(1) : '—' }),
    el('td', { class: 'num', text: m.context_length ? fmtNum(m.context_length) : '—' }),
    el('td', { class: 'num', text: m.max_output_tokens ? fmtNum(m.max_output_tokens) : '—' }),
    el('td', { text: (m.capabilities || []).join(', ') || '—' }),
    el('td', { text: (m.reasoning_efforts || []).join(', ') || '—' }),
  ])));
}

// ---------------------------------------------------------------- 日志

$('#logFilter').addEventListener('input', debounce(loadRequests, 300));
$('#logResult').addEventListener('change', loadRequests);

function debounce(fn, ms) {
  let t = null;
  return function () {
    clearTimeout(t);
    t = setTimeout(fn, ms);
  };
}

// 错误汇总：失败请求按 (账号 × 模型 × 状态) 聚合，最吵的排最前。
async function loadErrors() {
  try {
    const data = await api('/panel/api/error_summary');
    const rows = data.errors || [];
    const tbody = $('#errBody');
    $('#errNote').textContent = rows.length ? `${rows.length} 类` : '';
    if (!rows.length) {
      tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '6', class: 'empty', text: '近 24 小时没有失败' })]));
      return;
    }
    tbody.replaceChildren(...rows.map((e) => el('tr', { class: 'off' }, [
      el('td', { class: 'num', text: e.account || '—' }),
      el('td', { class: 'num', text: e.model || '—' }),
      el('td', {}, [el('span', { class: 'tag bad', text: String(e.status) })]),
      el('td', { class: 'num', text: fmtNum(e.count) }),
      el('td', { class: 'num', text: e.last || '—' }),
      el('td', { class: 'num', title: e.sample || null, text: (e.sample || '—').slice(0, 90) }),
    ])));
  } catch (err) {
    toast(err.message, 'bad');
  }
}

async function loadRequests() {
  const q = encodeURIComponent($('#logFilter').value.trim());
  const result = $('#logResult').value;
  try {
    const data = await api(`/panel/api/requests?limit=200&q=${q}&result=${result}`);
    const rows = data.records || [];
    const tbody = $('#reqBody');
    if (!rows.length) {
      tbody.replaceChildren(el('tr', {}, [el('td', { colspan: '8', class: 'empty', text: '暂无请求记录' })]));
      $('#reqSummary').textContent = '';
      return;
    }
    const fails = rows.filter((r) => !(r.status >= 200 && r.status < 300)).length;
    $('#reqSummary').textContent = `${rows.length} 条 · ${fails} 失败`;
    tbody.replaceChildren(...rows.map((r) => {
      const ok = r.status >= 200 && r.status < 300;
      return el('tr', { class: ok ? '' : 'off' }, [
        el('td', { class: 'num', text: fmtTime(r.t) }),
        // 失败原因挂 title：不占列宽，hover 看到上游原文
        el('td', {}, [el('span', { class: 'tag ' + (ok ? 'ok' : 'bad'), text: String(r.status), title: r.error || null })]),
        el('td', { class: 'num', text: r.model || '—' }),
        el('td', { class: 'num', text: r.account || '—' }),
        el('td', { class: 'num', text: (r.ip || '—') + (r.ua ? ' · ' + r.ua.slice(0, 24) : '') }),
        el('td', { class: 'num', text: fmtMs(r.latency_ms) }),
        el('td', { class: 'num', text: fmtNum((r.prompt || 0) + (r.completion || 0)) }),
        el('td', { class: 'num', text: fmtNum(r.points) }),
      ]);
    }));
  } catch (e) {
    toast(e.message, 'bad');
  }
}

// 频道筛选：chat=对话请求行 / task=面板运维动作 / sys=其余（与后端 Ring 归类一致）
let logCh = '';
$('#logChips').addEventListener('click', (e) => {
  const b = e.target.closest('.chip');
  if (!b) return;
  logCh = b.dataset.ch;
  $$('#logChips .chip').forEach((x) => x.classList.toggle('on', x === b));
  loadServiceLogs();
});

const CH_NAME = { chat: '对话', task: '任务', sys: '系统' };

async function loadServiceLogs() {
  try {
    const data = await api('/panel/api/logs');
    const box = $('#logBox');
    const list = (data.entries || []).filter((x) => !logCh || x.ch === logCh);
    box.replaceChildren(...(list.length
      ? list.map((x) => el('span', { class: 'ln' + (/\b(失败|错误|error)\b/i.test(x.text) ? ' e' : '') }, [
        el('i', { class: 'lch ' + x.ch, text: CH_NAME[x.ch] || x.ch }),
        el('span', { text: hhmmss(x.ts) + ' ' + x.text }),
      ]))
      : [el('span', { class: 'ln', text: '（该频道暂无日志）' })]));
    if ($('#logAutoscroll').checked) box.scrollTop = box.scrollHeight;
  } catch (e) {
    /* 日志页不阻断使用 */
  }
}

function hhmmss(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  const p = (n) => String(n).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
}

// ---------------------------------------------------------------- 配置

const CONFIG_FIELDS = [
  { title: '基础', fields: [
    { key: 'listen', label: '监听地址', type: 'text' },
    { key: 'default_model', label: '默认模型', type: 'text' },
    { key: 'upstream', label: '上游 base URL', type: 'text', restart: true },
    { key: 'account_base', label: '账号服务 URL', type: 'text', restart: true },
    { key: 'proxy', label: '全局代理', type: 'text' },
    { key: 'loomy_version', label: 'loomy-version 头', type: 'text' },
  ] },
  { title: '鉴权', fields: [
    { key: 'panel_password', label: '面板密码（留空 = 用 api_key）', type: 'password' },
  ] },
  { title: '账号池', fields: [
    { key: 'strategy', label: '选号策略 (weighted / round_robin / balance / lru)', type: 'text' },
    { key: 'pick_top_n', label: 'Top-N 短名单', type: 'number' },
    { key: 'max_inflight_per_account', label: '单账号在途上限 (0 = 不限)', type: 'number' },
    { key: 'balance_floor', label: '积分保底 (0 = 关闭)', type: 'number' },
    { key: 'sticky_ttl_seconds', label: '会话粘性 TTL (秒)', type: 'number' },
    { key: 'sticky_max_entries', label: '粘性表容量', type: 'number' },
  ] },
  { title: '冷却与熔断', fields: [
    { key: 'soft_rate_base_seconds', label: '软冷却基数 (秒)', type: 'number' },
    { key: 'soft_rate_max_seconds', label: '软冷却封顶 (秒)', type: 'number' },
    { key: 'not_found_cooldown_seconds', label: '404 冷却 (秒)', type: 'number' },
    { key: 'breaker_threshold', label: '熔断阈值 (连败次数)', type: 'number' },
    { key: 'breaker_cooldown_seconds', label: '熔断基数 (秒)', type: 'number' },
    { key: 'breaker_cooldown_max_seconds', label: '熔断封顶 (秒)', type: 'number' },
  ] },
  { title: '上游与请求', fields: [
    { key: 'timeout_seconds', label: '短 RPC 超时 (秒)', type: 'number' },
    { key: 'max_retries', label: '换号重试次数', type: 'number' },
    { key: 'keepalive_min_tokens', label: '长任务阈值 (max_tokens)', type: 'number' },
    { key: 'quota_refresh_minutes', label: '额度刷新间隔 (分)', type: 'number' },
    { key: 'session_renew_before_days', label: '提前续期天数', type: 'number' },
  ] },
  { title: '日志', fields: [
    { key: 'log_requests', label: '记录请求日志', type: 'bool' },
    { key: 'log_client_info', label: '记录调用来源 (IP / UA)', type: 'bool' },
    { key: 'usage_max_entries', label: '台账内存条数', type: 'number' },
    { key: 'strip_model_prefix', label: '自动补 imodel/ 前缀', type: 'bool' },
  ] },
];

async function loadConfig() {
  if (state.cfg) return;
  try {
    const data = await api('/panel/api/config');
    state.cfg = data.config || {};
    $('#cfgPath').textContent = data.path || '';
    renderConfig(state.cfg);
  } catch (e) {
    toast(e.message, 'bad');
  }
}

function renderConfig(cfg) {
  $('#cfgForm').replaceChildren(...CONFIG_FIELDS.map((g) => el('fieldset', {}, [
    el('legend', { text: g.title }),
    el('div', { class: 'grid2' }, g.fields.map((f) => {
      const v = cfg[f.key];
      if (f.type === 'bool') {
        const box = el('input', { type: 'checkbox', name: f.key });
        box.checked = !!v;
        return el('label', { class: 'switch' }, [el('span', { class: 'lb', text: f.label }), box]);
      }
      const shown = f.type === 'password' && v ? '' : (v === undefined || v === null ? '' : String(v));
      const input = el('input', {
        name: f.key,
        type: f.type === 'number' ? 'number' : f.type === 'password' ? 'password' : 'text',
        value: shown,
      });
      const hint = f.type === 'password' && v ? '已设置；留空表示不修改' : (f.restart ? '改动后需重启进程' : null);
      return el('label', { class: 'fld' }, [
        el('span', { class: 'lb', text: f.label }),
        input,
        hint ? el('span', { class: 'hint', text: hint }) : null,
      ]);
    })),
    el('div', { class: 'hr' }),
  ])));
}

$('#btnSaveCfg').addEventListener('click', async () => {
  const form = $('#cfgForm');
  const patch = {};
  for (const g of CONFIG_FIELDS) {
    for (const f of g.fields) {
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
    $('#cfgNote').textContent = res.message || '已保存';
    toast('配置已保存', 'ok');
    state.cfg = null;
    await refreshAll();
    await loadConfig();
  } catch (e) {
    toast(e.message, 'bad');
  }
});

// ---------------------------------------------------------------- 轮询

function startPolling() {
  if (state.timer) clearInterval(state.timer);
  state.timer = setInterval(() => {
    if (document.hidden) return;
    if (state.view === 'accounts') refreshAll().catch(() => {});
    else if (state.view === 'logs') loadServiceLogs();
  }, 5000);
}

// ---------------------------------------------------------------- 启动

(async function boot() {
  const v = location.hash.replace('#', '');
  if (TITLES[v]) switchView(v);
  try {
    await refreshAll();
    loadView(state.view);
  } catch (e) {
    openKey();
  }
  startPolling();
})();
