const $ = s => document.querySelector(s);
const $$ = s => document.querySelectorAll(s);
const basePath = window.__BASE__ || '';

// Track click position for modal transform-origin (Hero Transition from cursor)
let _clickX = window.innerWidth / 2, _clickY = window.innerHeight / 2;
document.addEventListener('mousedown', e => { _clickX = e.clientX; _clickY = e.clientY; }, true);

// Modal open/close with animation (born from click position)
let _modalAnimating = false;

function openModal(sel) {
  if (_modalAnimating) return;
  _modalAnimating = true;
  const overlay = typeof sel === 'string' ? $(sel) : sel;
  overlay.style.display = '';
  overlay.classList.remove('modal-closing');
  const modal = overlay.querySelector('.modal');
  if (modal) {
    // Measure real modal position
    modal.style.cssText = 'transition:none !important; transform:scale(1); opacity:0; pointer-events:none';
    const rect = modal.getBoundingClientRect();
    const ox = _clickX - rect.left;
    const oy = _clickY - rect.top;
    // Distance from click to modal center → duration based on speed
    const cx = rect.left + rect.width / 2;
    const cy = rect.top + rect.height / 2;
    const dist = Math.hypot(_clickX - cx, _clickY - cy);
    const dur = Math.max(0.2, Math.min(0.5, dist / 2500 + 0.15));
    // Reset and set origin + duration
    modal.style.cssText = '';
    modal.style.transformOrigin = `${ox}px ${oy}px`;
    modal.style.animationDuration = `${dur}s`;
    // Next frame: start animation
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        overlay.classList.add('modal-open');
        setTimeout(() => { _modalAnimating = false; }, dur * 1000);
      });
    });
  } else {
    overlay.classList.add('modal-open');
    _modalAnimating = false;
  }
}
function closeModal(sel) {
  if (_modalAnimating) return;
  _modalAnimating = true;
  const overlay = typeof sel === 'string' ? $(sel) : sel;
  const modal = overlay.querySelector('.modal');
  const closeDur = modal ? parseFloat(modal.style.animationDuration || '0.3') * 0.8 : 0.25;
  if (modal) modal.style.animationDuration = `${closeDur}s`;
  overlay.classList.remove('modal-open');
  overlay.classList.add('modal-closing');
  setTimeout(() => {
    overlay.style.display = 'none';
    overlay.classList.remove('modal-closing');
    if (modal) modal.style.animationDuration = '';
    // Reset all password fields to hidden when modal closes
    overlay.querySelectorAll('input[type="text"]').forEach(inp => {
      if (inp.closest('.pw-wrap')) inp.type = 'password';
    });
    _modalAnimating = false;
  }, closeDur * 1000 + 50);
}
const tokenKey = 'token:' + basePath;
function sha256(msg) {
  // Pure JS SHA-256 (works over HTTP, no crypto.subtle needed)
  const K = [
    0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
    0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
    0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
    0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
    0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
    0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
    0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
    0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2
  ];
  const r = (n, x) => (x >>> n) | (x << (32 - n));
  const bytes = new TextEncoder().encode(msg);
  const len = bytes.length, bits = len * 8;
  const pad = new Uint8Array(((len + 9 + 63) & ~63));
  pad.set(bytes); pad[len] = 0x80;
  const dv = new DataView(pad.buffer);
  dv.setUint32(pad.length - 4, bits, false);
  let [h0,h1,h2,h3,h4,h5,h6,h7] = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];
  for (let off = 0; off < pad.length; off += 64) {
    const w = new Int32Array(64);
    for (let i = 0; i < 16; i++) w[i] = dv.getInt32(off + i * 4, false);
    for (let i = 16; i < 64; i++) {
      const s0 = r(7,w[i-15]) ^ r(18,w[i-15]) ^ (w[i-15] >>> 3);
      const s1 = r(17,w[i-2]) ^ r(19,w[i-2]) ^ (w[i-2] >>> 10);
      w[i] = (w[i-16] + s0 + w[i-7] + s1) | 0;
    }
    let [a,b,c,d,e,f,g,h] = [h0,h1,h2,h3,h4,h5,h6,h7];
    for (let i = 0; i < 64; i++) {
      const S1 = r(6,e) ^ r(11,e) ^ r(25,e);
      const ch = (e & f) ^ (~e & g);
      const t1 = (h + S1 + ch + K[i] + w[i]) | 0;
      const S0 = r(2,a) ^ r(13,a) ^ r(22,a);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) | 0;
      h=g; g=f; f=e; e=(d+t1)|0; d=c; c=b; b=a; a=(t1+t2)|0;
    }
    h0=(h0+a)|0; h1=(h1+b)|0; h2=(h2+c)|0; h3=(h3+d)|0;
    h4=(h4+e)|0; h5=(h5+f)|0; h6=(h6+g)|0; h7=(h7+h)|0;
  }
  return [h0,h1,h2,h3,h4,h5,h6,h7].map(v=>(v>>>0).toString(16).padStart(8,'0')).join('');
}
// Set logo images and favicon to correct basePath
document.addEventListener('DOMContentLoaded', () => {
  const logoUrl = basePath + '/logo.svg';
  document.querySelectorAll('.logo-img').forEach(img => { img.src = logoUrl; });
  const fav = document.getElementById('favicon-link');
  if (fav) fav.href = logoUrl;
});

// ── Toast notifications ──
function toast(msg, type) {
  type = type || 'info';
  const el = document.createElement('div');
  el.className = `toast toast-${type}`;
  el.textContent = msg;
  $('#toast-container').appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; setTimeout(() => el.remove(), 300); }, 3000);
}

// ── Confirm dialog (returns Promise<bool>) ──
function showConfirm(title, msg) {
  return new Promise(resolve => {
    $('#confirm-title').textContent = title;
    $('#confirm-msg').textContent = msg;
    openModal('#confirm-modal');
    const ok = () => { cleanup(); resolve(true); };
    const cancel = () => { cleanup(); resolve(false); };
    const cleanup = () => {
      closeModal('#confirm-modal');
      $('#confirm-ok').onclick = null;
      $('#confirm-cancel').onclick = null;
    };
    $('#confirm-ok').onclick = ok;
    $('#confirm-cancel').onclick = cancel;
  });
}

// ── Password eye toggle ──
function togglePw(btn) {
  const inp = btn.parentElement.querySelector('input');
  if (inp.type === 'password') { inp.type = 'text'; btn.style.color = 'var(--primary)'; }
  else { inp.type = 'password'; btn.style.color = ''; }
}

function api(path, opts) {
  const headers = { ...(opts?.headers || {}) };
  const token = sessionStorage.getItem(tokenKey);
  if (token) headers['Authorization'] = 'Bearer ' + token;
  return fetch(basePath + '/api' + path, { ...opts, headers }).then(r => {
    if (r.status === 401) { sessionExpired(); throw 'session expired'; }
    if (!r.ok) return r.text().then(msg => { throw msg });
    return r.json();
  });
}

function esc(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

// ── Sidebar toggle (mobile) ──
function toggleSidebar() {
  document.querySelector('.sidebar').classList.toggle('open');
  document.getElementById('sidebar-overlay').classList.toggle('open');
}
function closeSidebar() {
  document.querySelector('.sidebar').classList.remove('open');
  document.getElementById('sidebar-overlay').classList.remove('open');
}

// ── Navigation / Router ──
const pageTitles = { nodes: 'nav.nodes', users: 'nav.users', proxies: 'nav.proxies', rules: 'nav.rules', tls: 'nav.tls', settings: 'nav.settings' };

let _currentPage = 'nodes';
let _localVersion = '1.0.0';
function switchPage(name, push) {
  if (!pageTitles[name]) name = 'nodes';
  _currentPage = name;
  closeSidebar();
  $$('.nav-item[data-page]').forEach(n => n.classList.toggle('active', n.dataset.page === name));
  $$('.page').forEach(p => p.style.display = 'none');
  const pageEl = $(`#page-${name}`);
  pageEl.style.display = '';
  // Re-trigger page enter animation
  pageEl.style.animation = 'none';
  pageEl.offsetHeight; // force reflow
  pageEl.style.animation = '';
  $('#page-title').textContent = t(pageTitles[name]);
  if (push !== false) history.pushState(null, '', basePath + '/' + name);
  if (name === 'users') { refreshUsers(); refreshSessions(); }
  if (name === 'proxies') refreshProxies();
  if (name === 'settings') loadSettings();
  if (name === 'rules') refreshRules();
  if (name === 'tls') refreshCerts();
}

function routeFromURL() {
  const path = location.pathname.replace(basePath, '').replace(/^\/+/, '');
  const seg = path.split('/')[0];
  if (seg === 'login' || !sessionStorage.getItem(tokenKey)) {
    showLogin();
    return;
  }
  showApp();
  switchPage(seg || 'nodes', false);
}

window.addEventListener('popstate', routeFromURL);

// ── Auth ──
function showLogin() {
  // Force full browser refresh to /login
  const loginURL = basePath + '/login';
  if (location.pathname !== loginURL) {
    location.href = loginURL;
    return;
  }
  $('#login-screen').style.display = '';
  $('#app').style.display = 'none';
  const saved = JSON.parse(localStorage.getItem('hy2scale_cred') || 'null');
  if (saved) {
    $('#login-user').value = saved.u;
    $('#login-pass').value = '********'; // visual indicator, actual hash used on submit
    $('#login-remember').checked = true;
  }
}
function showApp() {
  $('#login-screen').style.display = 'none';
  $('#app').style.display = '';
}

async function doLogin() {
  const username = $('#login-user').value.trim(), password = $('#login-pass').value;
  $('#login-error').textContent = '';
  try {
    // If password is the placeholder and we have saved hash, use it
    let passHash;
    const saved = JSON.parse(localStorage.getItem('hy2scale_cred') || 'null');
    if (password === '********' && saved && saved.h) {
      passHash = saved.h;
    } else {
      passHash = sha256(password);
    }
    const r = await fetch(basePath + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password: passHash }) });
    if (!r.ok) { $('#login-error').textContent = t('error.invalidCredentials'); return; }
    sessionStorage.setItem(tokenKey, (await r.json()).token);
    // Remember credentials (store hash, not plaintext)
    if ($('#login-remember').checked) {
      localStorage.setItem('hy2scale_cred', JSON.stringify({ u: username, h: passHash }));
    } else {
      localStorage.removeItem('hy2scale_cred');
    }
    showApp();
    history.pushState(null, '', basePath + '/nodes');
    switchPage('nodes', false);
    refresh();
  } catch (e) { $('#login-error').textContent = String(e); }
}
function doLogout() {
  sessionStorage.removeItem(tokenKey);
  // Keep hy2scale_cred for "remember me" pre-fill on next login
  clearInterval(pollTimer);
  showLogin();
}
function sessionExpired() {
  sessionStorage.removeItem(tokenKey);
  clearInterval(pollTimer);
  showLogin();
}
$('#login-pass').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
$('#login-user').addEventListener('keydown', e => { if (e.key === 'Enter') $('#login-pass').focus(); });

// ── Polling ──
let pollTimer = null, lastTopoJSON = '', lastTopoStructKey = '';
let connectedPeers = new Set(); // updated from topology

let proxiesLoaded = false;
async function refresh() {
  try {
    const node = await api('/node');
    $('#node-badge').textContent = node.node_id;
    $('#node-name-display').textContent = node.name !== node.node_id ? node.name : '';
    _localVersion = node.version || '1.0.0';
    if (node.version) {
      const vb = $('#version-badge');
      vb.classList.remove('limited', 'compat');
      if (node.limited) {
        vb.textContent = 'v' + node.version + ' Limited';
        vb.classList.add('limited');
      } else if (node.compat) {
        vb.textContent = 'v' + node.version + ' Compat';
        vb.classList.add('compat');
      } else {
        vb.textContent = 'v' + node.version;
      }
    }
    const tasks = [refreshTopology(), refreshStats()];
    if (!proxiesLoaded) { tasks.push(refreshProxies().catch(()=>{})); proxiesLoaded = true; }
    // Live-refresh based on current page
    if (_currentPage === 'users') tasks.push(refreshSessions().catch(()=>{}));
    if (_currentPage === 'proxies') tasks.push(loadWireGuard().catch(()=>{}));
    await Promise.all(tasks);
  } catch (e) { console.error(e); }
  clearInterval(pollTimer);
  pollTimer = setInterval(refresh, 5000);
}

// ── Stats ──
function fmtBytes(b) {
  if (b < 1024) return b + ' B';
  if (b < 1048576) return (b / 1024).toFixed(1) + ' KB';
  if (b < 1073741824) return (b / 1048576).toFixed(1) + ' MB';
  return (b / 1073741824).toFixed(2) + ' GB';
}
function fmtRate(b) { return fmtBytes(b) + '/s'; }

async function refreshStats() {
  try {
    const s = await api('/stats');
    $('#s-tx-total').textContent = fmtBytes(s.tx_bytes);
    $('#s-rx-total').textContent = fmtBytes(s.rx_bytes);
    $('#s-tx-rate').textContent = fmtRate(s.tx_rate);
    $('#s-rx-rate').textContent = fmtRate(s.rx_rate);
    $('#s-conns').textContent = s.conns;
    $('#s-exits').textContent = s.exit_clients;
  } catch (e) {}
}

// ── Topology Table ──
const syncingNodes = new Map(); // name -> { cachedLatency }

function latencyHTML(ms, name) {
  if (syncingNodes.has(name)) return '<span class="latency latency-sync">' + t('nodes.syncing') + '</span>';
  if (ms === -1) return '<span class="latency latency-off">' + t('nodes.offline') + '</span>';
  if (ms === 0) return '<span class="latency latency-na">—</span>';
  const cls = ms < 80 ? 'latency-good' : ms < 200 ? 'latency-med' : 'latency-bad';
  return `<span class="latency ${cls}">${ms}ms</span>`;
}

function isSyncing(name) { return syncingNodes.has(name); }

function trafficHTML(tx, rx) {
  return `<span class="stat-up">${fmtRate(tx||0)}</span> <span style="color:var(--text-muted)">/</span> <span class="stat-down">${fmtRate(rx||0)}</span>`;
}

function dirHTML(dir) {
  if (dir === 'local') return '<span class="badge badge-green">LOCAL</span>';
  return dir === 'inbound'
    ? '<span class="badge badge-blue">IN</span>'
    : '<span class="badge badge-orange">OUT</span>';
}

function remoteURL(chain) {
  return basePath + '/remote/' + chain.map(encodeURIComponent).join('/') + '/';
}

function nameLink(name, chain) {
  if (!chain || !chain.length || window.__PROXY__) return `<span class="peer-name-cell">${esc(name)}</span>`;
  return `<a class="peer-name-cell peer-link" href="${remoteURL(chain)}" target="_blank">${esc(name)}</a>`;
}

function parentRowHTML(n) {
  if (n.is_self) {
    return `<tr class="self-row${n.disabled ? ' disabled' : ''}">
      <td><label class="toggle"><input type="checkbox" ${n.disabled ? '' : 'checked'} onchange="toggleSelfDisable(!this.checked)"><span class="slider"></span></label></td>
      <td class="col-status"><span class="latency latency-good">∞ms</span></td>
      <td class="col-dir">${dirHTML('local')}</td>
      <td class="col-name">
        <span class="peer-name-cell">${esc(n.name)}</span>
        ${n.addr ? `<span class="peer-addr-sub">${esc(n.addr)} (UDP)</span>` : `<span class="peer-addr-sub">${t('nodes.noHy2Server')}</span>`}
      </td>
      <td class="col-traffic">${trafficHTML(0, 0)}</td>
      <td class="col-nested"><label class="toggle toggle-disabled"><input type="checkbox" checked disabled><span class="slider"></span></label></td>
      <td class="col-actions"><div class="act-group">
        <button class="act-btn edit" onclick="openEditSelf()">${t('app.edit')}</button>
      </div></td>
    </tr>`;
  }

  const chain = n.native ? [] : [n.name];
  const nativeBadge = n.native ? ' <span class="badge badge-muted">NATIVE</span>' : '';
  const versionBadge = (n.version && n.version !== _localVersion) ? ` <span class="badge badge-warn">v${esc(n.version)}</span>` : '';
  const syncing = syncingNodes.has(n.name);
  const syncData = syncingNodes.get(n.name);
  const nestedChecked = syncing ? syncData.enabled : n.nested;
  const nested = n.native
    ? '<label class="toggle toggle-disabled"><input type="checkbox" disabled><span class="slider"></span></label>'
    : (n.direction === 'outbound'
      ? `<label class="toggle"><input type="checkbox" ${nestedChecked ? 'checked' : ''} onchange="toggleNested('${esc(n.name)}',this.checked)"><span class="slider"></span></label>`
      : '');
  const toggleCol = n.direction === 'outbound'
    ? `<label class="toggle"><input type="checkbox" ${n.disabled ? '' : 'checked'} onchange="toggleDisable('${esc(n.name)}',!this.checked)"><span class="slider"></span></label>`
    : '';
  const actions = n.direction === 'outbound' ? `<div class="act-group">
    <button class="act-btn edit" onclick="openEditDialog('${esc(n.name)}')">${t('app.edit')}</button>
    <button class="act-btn danger" onclick="removeClient('${esc(n.name)}')">${t('app.delete')}</button>
  </div>` : '';

  return `<tr class="${n.disabled ? 'disabled' : ''} ${syncing ? 'syncing' : ''}">
    <td>${toggleCol}</td>
    <td class="col-status">${latencyHTML(n.latency_ms, n.name)}</td>
    <td class="col-dir">${dirHTML(n.direction)}</td>
    <td class="col-name">
      ${n.native ? `<span class="peer-name-cell peer-rename" onclick="renameNative('${esc(n.name)}')">${esc(n.name)}</span>` : nameLink(n.name, chain)}${nativeBadge}${versionBadge}
      ${n.addr ? `<span class="peer-addr-sub">${esc(n.addr)}${n.addrs && n.addrs.length > 1 ? ` <span class="badge badge-muted addr-more-badge" data-ipstatus='${JSON.stringify(n.ip_statuses || n.addrs.map(a=>({addr:a,status:n.connected?"online":"offline"})))}' data-lat="${n.latency_ms || 0}">+${n.addrs.length - 1}</span>` : ''}</span>` : ''}
    </td>
    <td class="col-traffic">${trafficHTML(n.tx_rate, n.rx_rate)}</td>
    <td class="col-nested">${nested}</td>
    <td class="col-actions">${actions}</td>
  </tr>`;
}

// guides: array of booleans per depth, true = draw continuation vertical line at that depth
function childRowHTML(c, isLast, depth, parentChain, guides) {
  depth = depth || 1;
  parentChain = parentChain || [];
  guides = guides || [];
  const chain = c.native ? [] : [...parentChain, c.name];
  const dis = isNestedDisabled(c.via, c.name);
  const dir = c.direction ? dirHTML(c.direction) : '';
  const nativeBadge = c.native ? ' <span class="badge badge-muted">NATIVE</span>' : '';
  const cVersionBadge = (c.version && c.version !== _localVersion) ? ` <span class="badge badge-warn">v${esc(c.version)}</span>` : '';
  // Full path key: parentChain includes all ancestors, use it for unique qualified key
  const cFullPath = [...parentChain, c.name].join('/');
  const cSyncing = syncingNodes.has(cFullPath);
  const cSyncData = syncingNodes.get(cFullPath);
  const cNestedChecked = cSyncing ? cSyncData.enabled : c.nested;
  const nestedToggle = c.native
    ? '<label class="toggle toggle-disabled"><input type="checkbox" disabled><span class="slider"></span></label>'
    : `<label class="toggle"><input type="checkbox" ${cNestedChecked ? 'checked' : ''} onchange="toggleNested('${esc(cFullPath)}',this.checked)"><span class="slider"></span></label>`;
  const nameCell = c.native
    ? `<span class="peer-name-cell">${esc(c.name)}</span>`
    : nameLink(c.name, chain);
  const childToggle = `<label class="toggle"><input type="checkbox" ${dis ? '' : 'checked'} onchange="toggleNestedDisable('${esc(c.via)}','${esc(c.name)}',!this.checked)"><span class="slider"></span></label>`;

  // Build guide lines for ancestor depths + current branch
  let treeHTML = '';
  for (let d = 0; d < depth - 1; d++) {
    treeHTML += `<span class="tree-guide${guides[d] ? ' tree-guide-active' : ''}" aria-hidden="true"></span>`;
  }
  treeHTML += `<span class="tree-branch${isLast ? ' tree-last' : ''}" aria-hidden="true"></span>`;

  let html = `<tr class="sub-row${dis ? ' disabled' : ''}${cSyncing ? ' syncing' : ''}">
    <td>${childToggle}</td>
    <td class="col-status">${dis ? latencyHTML(-1, c.name) : latencyHTML(c.latency_ms, c.name)}</td>
    <td class="col-dir">${dir}</td>
    <td class="col-name">
      ${treeHTML}<span class="sub-name-wrap">
        ${nameCell}${nativeBadge}${cVersionBadge}
        <span class="peer-addr-sub">via ${esc(c.via)}</span>
      </span>
    </td>
    <td class="col-traffic">${trafficHTML(c.tx_rate, c.rx_rate)}</td>
    <td class="col-nested">${nestedToggle}</td>
    <td class="col-actions"></td>
  </tr>`;

  if (c.children?.length) {
    const childGuides = [...guides, !isLast]; // propagate: "my parent is not last"
    for (let i = 0; i < c.children.length; i++) {
      html += childRowHTML(c.children[i], i === c.children.length - 1, depth + 1, chain, childGuides);
    }
  }
  return html;
}

function topoStructureKey(topo) {
  // Generate a key that only changes on structural changes, not latency
  function nodeKey(n) {
    let k = n.name + '|' + n.connected + '|' + n.nested + '|' + n.disabled + '|' + (n.native||'') + '|' + (n.version||'');
    if (n.children) k += '[' + n.children.map(nodeKey).join(',') + ']';
    return k;
  }
  return topo.map(nodeKey).join(';');
}

async function refreshTopology() {
  const topo = await api('/topology');
  const json = JSON.stringify(topo);
  if (json === lastTopoJSON) return;

  // Check if user is selecting text — skip DOM update to preserve selection
  const sel = window.getSelection();
  const hasSelection = sel && sel.toString().length > 0;
  const structKey = topoStructureKey(topo);
  if (hasSelection && structKey === lastTopoStructKey) {
    // Only latency changed, user is selecting — skip re-render
    lastTopoJSON = json;
    return;
  }
  lastTopoJSON = json;
  lastTopoStructKey = structKey;

  // Build connected peers set from all visible nodes
  const newConnected = new Set();
  function collectPeers(nodes, parentReachable) {
    for (const n of nodes) {
      const reachable = n.connected || n.is_self || n.native || n.latency_ms > 0 || parentReachable;
      if (reachable) newConnected.add(n.name);
      if (n.children) collectPeers(n.children, reachable);
    }
  }
  collectPeers(topo, false);
  connectedPeers = newConnected;
  buildExitPaths(topo);

  // Cache latencies and clear syncing for nodes with children
  for (const n of topo) {
  }

  const el = $('#topology-tree');
  if (!topo?.length) {
    el.innerHTML = '<div class="empty" data-i18n="nodes.noConnections" data-i18n-html>' + t('nodes.noConnections') + '</div>';
    $('#peer-count').textContent = '0';
    return;
  }

  let count = 0, rows = '';
  for (const n of topo) {
    count++;
    rows += parentRowHTML(n);
    if (n.children?.length) {
      for (let i = 0; i < n.children.length; i++) {
        count++;
        const parentChain = n.is_self ? [] : [n.name];
        rows += childRowHTML(n.children[i], i === n.children.length - 1, 1, parentChain, []);
      }
    }
  }

  const scrollEl = el.querySelector('.table-scroll');
  const scrollLeft = scrollEl ? scrollEl.scrollLeft : 0;
  el.innerHTML = `<div class="table-scroll"><table class="peer-table">
    <thead><tr>
      <th style="width:50px">${t('users.on')}</th>
      <th class="col-status">${t('nodes.status')}</th>
      <th class="col-dir">${t('nodes.dir')}</th>
      <th class="col-name">${t('nodes.node')}</th>
      <th class="col-traffic">${t('nodes.traffic')}</th>
      <th class="col-nested">${t('nodes.nested')}</th>
      <th class="col-actions"></th>
    </tr></thead>
    <tbody>${rows}</tbody>
  </table></div>`;
  if (scrollLeft) { const s = el.querySelector('.table-scroll'); if (s) s.scrollLeft = scrollLeft; }
  $('#peer-count').textContent = count;
}

async function toggleNested(name, enabled) {
  syncingNodes.set(name, { enabled });
  lastTopoJSON = ''; refreshTopology(); // immediate: gray + syncing + toggled checkbox

  try {
    await api(`/peers/${encodeURIComponent(name)}/nested`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }) });
    // Poll until topology reflects the actual change
    const poll = setInterval(async () => {
      try {
        const topo = await api('/topology');
        // Search by full path: name = "AUB/jp-t1/cn-shandong"
        const parts = name.split('/');
        let nodes = topo;
        let found = null;
        for (let i = 0; i < parts.length; i++) {
          const p = parts[i];
          const match = nodes ? nodes.find(n => n.name === p) : null;
          if (!match) break;
          if (i === parts.length - 1) found = match;
          else nodes = match.children;
        }
        if (!found) return;
        if (found.nested === enabled) {
          clearInterval(poll);
          syncingNodes.delete(name);
          lastTopoJSON = '';
          refreshTopology();
        }
      } catch(e) {}
    }, 1000);
    // Safety timeout
    setTimeout(() => { clearInterval(poll); syncingNodes.delete(name); lastTopoJSON = ''; refreshTopology(); }, 15000);
  } catch (e) { syncingNodes.delete(name); toast(String(e), 'error'); lastTopoJSON = ''; refreshTopology(); }
}

async function toggleDisable(name, disabled) {
  try { await api(`/clients/${name}/disable`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ disabled }) }); lastTopoJSON = ''; refreshTopology(); }
  catch (e) { toast(String(e), 'error'); }
}

// Rename native peer (display name only, stored locally)
async function renameNative(currentName) {
  const input = document.createElement('input');
  input.value = currentName;
  input.style.cssText = 'width:200px';
  const container = document.createElement('div');
  container.appendChild(input);
  $('#confirm-msg').textContent = '';
  $('#confirm-msg').appendChild(container);
  $('#confirm-title').textContent = t('nodes.renameTitle');
  $('#confirm-ok').style.background = 'var(--primary)';
  $('#confirm-ok').style.borderColor = 'var(--primary)';
  $('#confirm-ok').textContent = t('app.save');
  openModal('#confirm-modal');
  input.focus();
  input.select();
  const ok = await new Promise(resolve => {
    const done = (v) => { closeModal('#confirm-modal'); $('#confirm-ok').style.background = ''; $('#confirm-ok').style.borderColor = ''; $('#confirm-ok').textContent = t('app.confirm'); resolve(v); };
    $('#confirm-ok').onclick = () => done(true);
    $('#confirm-cancel').onclick = () => done(false);
    input.addEventListener('keydown', e => { if (e.key === 'Enter') done(true); if (e.key === 'Escape') done(false); });
  });
  if (!ok) return;
  const newName = input.value.trim();
  if (!newName || newName === currentName) return;
  try {
    // Update client name via API
    const cl = await api(`/clients/${encodeURIComponent(currentName)}`);
    cl.name = newName;
    await api(`/clients/${encodeURIComponent(currentName)}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(cl) });
    toast(t('nodes.renamed', {name: newName}), 'success');
    lastTopoJSON = ''; refreshTopology();
  } catch (e) { toast(String(e), 'error'); }
}

// Nested peer disable: stop pinging/using this peer via the parent
const nestedDisabled = JSON.parse(sessionStorage.getItem('nestedDisabled') || '{}');
function isNestedDisabled(via, name) { return !!nestedDisabled[via + '/' + name]; }
function toggleNestedDisable(via, name, disabled) {
  const key = via + '/' + name;
  if (disabled) nestedDisabled[key] = true; else delete nestedDisabled[key];
  sessionStorage.setItem('nestedDisabled', JSON.stringify(nestedDisabled));
  lastTopoJSON = ''; refreshTopology();
}

async function removeClient(name) {
  if (!await showConfirm(t('nodes.deleteTitle'), t('nodes.deleteConfirm', {name}))) return;
  try { await api(`/clients/${encodeURIComponent(name)}`, { method: 'DELETE' }); lastTopoJSON = ''; refreshTopology(); toast(t('nodes.deleted', {name}), 'success'); }
  catch (e) { toast(String(e), 'error'); }
}

// ── Node Modal (Add / Edit) ──
let editingNode = null; // null = add mode, string = edit mode (name)

async function loadCaCertOptions(currentCa) {
  const sel = $('#add-ca-select');
  const ta = $('#add-ca');
  sel.innerHTML = '<option value="">None (use system CA)</option>';
  try {
    const certs = await api('/tls');
    for (const c of certs) {
      if (c.is_ca) {
        const opt = document.createElement('option');
        opt.value = c.id;
        opt.textContent = c.name || c.id;
        sel.appendChild(opt);
      }
    }
  } catch(e) {}
  // Add manual paste option
  const manualOpt = document.createElement('option');
  manualOpt.value = '__manual__';
  manualOpt.textContent = '— Paste PEM manually —';
  sel.appendChild(manualOpt);

  // Set current value
  if (currentCa && currentCa.startsWith('-----')) {
    sel.value = '__manual__';
    ta.value = currentCa;
    ta.style.display = '';
  } else if (currentCa) {
    sel.value = currentCa;
    ta.style.display = 'none';
  } else {
    sel.value = '';
    ta.value = '';
    ta.style.display = 'none';
  }

  sel.onchange = () => {
    if (sel.value === '__manual__') {
      ta.style.display = '';
      ta.focus();
    } else {
      ta.style.display = 'none';
      ta.value = '';
    }
  };
}

function getSelectedCa() {
  const sel = $('#add-ca-select');
  if (sel.value === '__manual__') return $('#add-ca').value.trim();
  return sel.value; // cert ID or empty
}

function switchNodeTab(btn, panelId) {
  btn.parentElement.querySelectorAll('.proxy-tab').forEach(t => t.classList.remove('active'));
  btn.classList.add('active');
  btn.closest('.modal-body').querySelectorAll('.node-tab-panel').forEach(p => {
    p.style.display = 'none';
    p.style.animation = '';
  });
  const panel = $('#' + panelId);
  panel.style.display = '';
  panel.style.animation = 'tabSlideIn .35s ease both';
}

function addAddrRow(host, port) {
  const list = $('#addr-list');
  const idx = list.children.length;
  const row = document.createElement('div');
  row.className = 'addr-row';
  row.innerHTML = `<input class="addr-ip" placeholder="IP or hostname" value="${host || ''}">
    <input class="addr-port" placeholder="Port(s)" value="${port || ''}">
    <button class="addr-del" tabindex="-1" onclick="removeAddrRow(this)" ${idx === 0 ? 'disabled' : ''}>&#8722;</button>`;
  list.appendChild(row);
  updateAddrDelButtons();
  syncConnMode();
}

function removeAddrRow(btn) {
  const row = btn.closest('.addr-row');
  row.remove();
  updateAddrDelButtons();
  syncConnMode();
}

function updateAddrDelButtons() {
  const rows = $$('#addr-list .addr-row');
  rows.forEach((row, i) => {
    row.querySelector('.addr-del').disabled = (rows.length <= 1);
  });
}

// Sync connection mode radio buttons with address count
function syncConnMode() {
  const addrCount = $$('#addr-list .addr-row').length;
  const panel = $('#conn-mode-panel');
  const radios = panel.querySelectorAll('input[type=radio]');
  const directRadio = panel.querySelector('input[value=""]');
  const stabilityRadio = panel.querySelector('input[value="stability"]');

  if (addrCount <= 1) {
    // Single IP: force Direct, disable others
    directRadio.checked = true;
    radios.forEach(r => {
      r.disabled = true;
    });
    panel.classList.add('exit-mode-disabled');
  } else {
    // Multi IP: disable Direct, enable Stability/Speed
    directRadio.disabled = true;
    stabilityRadio.disabled = false;
    panel.querySelector('input[value="speed"]').disabled = false;
    panel.classList.remove('exit-mode-disabled');
    // If Direct was selected, switch to Stability
    if (directRadio.checked) {
      stabilityRadio.checked = true;
    }
  }
}

function getConnMode() {
  const sel = $('#conn-mode-panel').querySelector('input[type=radio]:checked');
  return sel ? sel.value : '';
}

function setConnMode(mode) {
  const panel = $('#conn-mode-panel');
  panel.querySelectorAll('input[type=radio]').forEach(r => { r.checked = r.value === (mode || ''); });
  syncConnMode();
}

function getAddrList() {
  const rows = [...$$('#addr-list .addr-row')];
  return rows.map(row => {
    const ip = row.querySelector('.addr-ip').value.trim();
    const port = row.querySelector('.addr-port').value.trim();
    return { ip, port };
  }).filter(a => a.ip || a.port);
}

// Validate port spec: single port, comma-separated, or ranges (e.g. "5565", "1000,2000", "20000-30000")
function validatePortSpec(spec) {
  if (!spec) return false;
  const parts = spec.split(',').map(s => s.trim()).filter(Boolean);
  if (!parts.length) return false;
  for (const p of parts) {
    if (p.includes('-')) {
      const [a, b] = p.split('-').map(s => parseInt(s.trim()));
      if (isNaN(a) || isNaN(b) || a < 1 || b > 65535 || a > b) return false;
    } else {
      const n = parseInt(p);
      if (isNaN(n) || n < 1 || n > 65535) return false;
    }
  }
  return true;
}

function validateAddrs() {
  const errEl = $('#addr-error');
  errEl.style.display = 'none';
  const rows = $$('#addr-list .addr-row');

  function fail(msg) {
    errEl.textContent = msg;
    errEl.style.display = '';
    // Switch to Addresses tab so user sees the error
    switchNodeTab($$('#add-node-modal .proxy-tab')[0], 'node-tab-addrs');
    toast(msg, 'error');
  }

  for (const row of rows) {
    const ip = row.querySelector('.addr-ip').value.trim();
    const port = row.querySelector('.addr-port').value.trim();
    row.querySelector('.addr-ip').style.borderColor = '';
    row.querySelector('.addr-port').style.borderColor = '';
    if (!ip) {
      row.querySelector('.addr-ip').style.borderColor = 'var(--red)';
      fail('IP/hostname is required for each address');
      return null;
    }
    if (!port || !validatePortSpec(port)) {
      row.querySelector('.addr-port').style.borderColor = 'var(--red)';
      fail('Invalid port format. Use: 5565 or 1000,2000 or 20000-30000');
      return null;
    }
  }
  // Check duplicates
  const addrStrs = getAddrList().map(a => `${a.ip}:${a.port}`);
  const seen = new Set();
  for (let i = 0; i < addrStrs.length; i++) {
    if (seen.has(addrStrs[i])) {
      const dupRow = rows[i];
      dupRow.querySelector('.addr-ip').style.borderColor = 'var(--red)';
      fail('Duplicate address: ' + addrStrs[i]);
      return null;
    }
    seen.add(addrStrs[i]);
  }
  return addrStrs;
}

function openAddDialog() {
  editingNode = null;
  $('#add-node-modal-title').textContent = t('nodes.addTitle');
  $('#add-node-submit').textContent = t('app.save');
  ['add-pass','add-sni','add-ca','add-tx','add-rx','add-isw','add-msw','add-icw','add-mcw'].forEach(id => $(`#${id}`).value = '');
  $('#add-insecure').checked = true; $('#add-fastopen').checked = false;
  $('#addr-list').innerHTML = '';
  $('#addr-error').style.display = 'none';
  setConnMode('');
  addAddrRow('', '');
  loadCaCertOptions('');
  switchNodeTab($('#add-node-modal .proxy-tab'), 'node-tab-addrs');
  openModal('#add-node-modal');
  $('#quic-advanced').style.display = 'none';
}

async function openEditDialog(name) {
  try {
    const cl = await api(`/clients/${encodeURIComponent(name)}`);
    editingNode = name;
    $('#add-node-modal-title').textContent = t('nodes.editPrefix', {name});
    $('#add-node-submit').textContent = t('app.save');
    // Populate address list
    $('#addr-list').innerHTML = '';
    $('#addr-error').style.display = 'none';
    const addrs = cl.addrs && cl.addrs.length ? cl.addrs : (cl.addr ? [cl.addr] : ['']);
    for (const a of addrs) {
      const parts = a.match(/^(.+):(.+)$/);
      if (parts) addAddrRow(parts[1], parts[2]);
      else addAddrRow(a, '');
    }
    setConnMode(cl.conn_mode || '');
    $('#add-pass').value = cl.password || '';
    $('#add-sni').value = cl.sni || '';
    $('#add-insecure').checked = cl.insecure !== false;
    loadCaCertOptions(cl.ca || '');
    $('#add-tx').value = cl.max_tx ? (cl.max_tx / 125000).toFixed(0) : '';
    $('#add-rx').value = cl.max_rx ? (cl.max_rx / 125000).toFixed(0) : '';
    $('#add-isw').value = cl.init_stream_window || '';
    $('#add-msw').value = cl.max_stream_window || '';
    $('#add-icw').value = cl.init_conn_window || '';
    $('#add-mcw').value = cl.max_conn_window || '';
    $('#add-fastopen').checked = !!cl.fast_open;
    switchNodeTab($('#add-node-modal .proxy-tab'), 'node-tab-addrs');
    openModal('#add-node-modal');
    const hasQuic = cl.init_stream_window || cl.max_stream_window || cl.init_conn_window || cl.max_conn_window;
    $('#quic-advanced').style.display = hasQuic ? '' : 'none';
  } catch (e) { toast(String(e), 'error'); }
}

function closeAddDialog() { closeModal('#add-node-modal'); editingNode = null; }

async function submitAddNode() {
  const addrs = validateAddrs();
  if (!addrs) return;
  const password = $('#add-pass').value.trim();
  if (!addrs.length) { toast(t('nodes.addrPassRequired'), 'error'); return; }
  if (!password) { switchNodeTab($$('#add-node-modal .proxy-tab')[1], 'node-tab-conn'); toast('Password is required', 'error'); return; }
  const addr = addrs[0];
  const name = editingNode || addr;
  const body = {
    name, addr, addrs, password, conn_mode: getConnMode(),
    sni: $('#add-sni').value.trim(), insecure: $('#add-insecure').checked, ca: getSelectedCa(),
    max_tx: Math.round((parseFloat($('#add-tx').value) || 0) * 125000),
    max_rx: Math.round((parseFloat($('#add-rx').value) || 0) * 125000),
    init_stream_window: parseInt($('#add-isw').value) || 0, max_stream_window: parseInt($('#add-msw').value) || 0,
    init_conn_window: parseInt($('#add-icw').value) || 0, max_conn_window: parseInt($('#add-mcw').value) || 0,
    fast_open: $('#add-fastopen').checked,
  };
  try {
    if (editingNode) {
      await api(`/clients/${encodeURIComponent(editingNode)}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    } else {
      await api('/clients', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    }
    const wasEdit = !!editingNode;
    closeAddDialog();
    toast(wasEdit ? t('nodes.updated', {name}) : t('nodes.saved', {name}), 'success');
    lastTopoJSON = ''; setTimeout(refreshTopology, 1000);
  } catch (e) { toast(String(e), 'error'); }
}

// ── Edit Self Modal ──
async function openEditSelf() {
  try {
    const [n, certs] = await Promise.all([api('/node'), api('/tls')]);
    $('#self-nodeid').value = n.node_id || '';
    const listenAddr = n.server?.listen || '0.0.0.0:5565';
    const listenParts = listenAddr.match(/^(.+):(.+)$/);
    $('#self-srv-ip').value = listenParts ? listenParts[1] : '0.0.0.0';
    $('#self-srv-port').value = listenParts ? listenParts[2] : '5565';
    $('#self-srv-pass').value = n.server?.password || '';
    // Populate TLS cert dropdown
    const sel = $('#self-srv-tls');
    const currentCert = n.server?.tls_cert || '';
    sel.innerHTML = `<option value="">${t('nodes.selfSignedAuto')}</option>`;
    if (certs?.length) {
      for (const c of certs) {
        if (!c.key_file) continue; // need private key
        const selected = c.cert_file === currentCert ? ' selected' : '';
        sel.innerHTML += `<option value="${esc(c.id)}"${selected}>${esc(c.name)} (${esc(c.subject)})</option>`;
      }
    }
    $('#self-error').textContent = '';
    $('#self-ok').textContent = '';
    openModal('#edit-self-modal');
  } catch (e) { toast(String(e), 'error'); }
}
function closeEditSelf() { closeModal('#edit-self-modal'); }

async function submitEditSelf() {
  $('#self-error').textContent = ''; $('#self-ok').textContent = '';
  const nodeId = $('#self-nodeid').value.trim();
  const srvIp = $('#self-srv-ip').value.trim() || '0.0.0.0';
  const srvPort = $('#self-srv-port').value.trim() || '5565';
  const srvListen = srvIp + ':' + srvPort;
  const srvPass = $('#self-srv-pass').value.trim();
  const tlsId = $('#self-srv-tls').value;

  if (!nodeId) { $('#self-error').textContent = t('nodes.nodeIdRequired'); return; }

  const body = { node_id: nodeId, name: nodeId, exit_node: true };
  if (srvListen || srvPass) {
    const srv = { listen: srvListen || '0.0.0.0:5565', password: srvPass, tls_cert: '', tls_key: '' };
    if (tlsId) {
      // Resolve cert/key paths from TLS store
      srv.tls_cert = `/data/tls/${tlsId}.crt`;
      srv.tls_key = `/data/tls/${tlsId}.key`;
    }
    body.server = srv;
  } else {
    body.server = { listen: '', password: '' };
  }

  try {
    await api('/node', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    toast(t('nodes.settingsSaved'), 'success');
    $('#node-badge').textContent = nodeId;
    $('#node-name-display').textContent = '';
    lastTopoJSON = ''; refreshTopology();
  } catch (e) { $('#self-error').textContent = String(e); }
}

async function toggleSelfDisable(disabled) {
  try {
    if (disabled) {
      // Clear server config to disable hy2 server
      await api('/node', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ server: { listen: '', password: '' } }) });
    }
    // Mark in config
    await api('/node', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ exit_node: !disabled }) });
    lastTopoJSON = ''; refreshTopology();
  } catch (e) { toast(String(e), 'error'); }
}

// ── Proxies ──
function switchProxyTab(tab) {
  $$('.proxy-tab').forEach(t => t.classList.toggle('active', t === tab));
  $$('.proxy-panel').forEach(p => {
    const show = p.id === 'ptab-' + tab.dataset.ptab;
    p.classList.toggle('active', show);
    p.style.display = show ? '' : 'none';
    if (show) { p.style.animation = 'none'; p.offsetHeight; p.style.animation = ''; }
  });
  refreshProxies();
}

async function refreshIKEv2Certs() {
  try {
    const certs = await api('/tls');
    const sel = $('#ikev2-cert');
    const cur = sel.value;
    sel.innerHTML = `<option value="">${t('ikev2.selectCACert')}</option>`;
    for (const c of certs) {
      if (!c.is_ca || !c.key_file) continue; // Only show CA certs with private key
      const opt = document.createElement('option');
      opt.value = c.id;
      opt.textContent = c.name || c.id;
      if (c.id === cur) opt.selected = true;
      sel.appendChild(opt);
    }
  } catch(e) {}
}

async function refreshProxies() {
  // Load Hysteria status
  try {
    const node = await api('/node');
    const srv = node.server;
    $('#hy2-port').value = srv?.listen?.replace(/.*:/, '') || '5565';
    $('#hy2-status').value = srv ? 'Enabled' : 'Disabled';
    $('#hy2-user-auth').checked = node.hy2_user_auth || false;
  } catch(e) {}
  // Load SOCKS5 config
  try {
    const proxies = await api('/proxies');
    const sk5 = proxies?.find(p => p.protocol === 'socks5');
    if (sk5) {
      $('#sk5-port').value = sk5.listen?.replace(/.*:/, '') || '';
      $('#sk5-enabled').checked = sk5.enabled;
    }
  } catch(e) {}
  // Load SS config
  try {
    const ss = await api('/ss');
    $('#ss-port').value = ss.listen?.replace(/.*:/, '') || '';
    $('#ss-enabled').checked = ss.enabled;
    if (ss.method) $('#ss-method').value = ss.method;
  } catch(e) {}
  // Load L2TP config
  try {
    const l = await api('/l2tp');
    $('#l2tp-port').value = l.listen || '1701';
    $('#l2tp-enabled').checked = l.enabled;
    $('#l2tp-pool').value = l.pool || '';
    $('#l2tp-psk').value = l.psk || '';
    $('#l2tp-mtu').value = l.mtu || 1280;
    // Capability check
    const panel = $('#ptab-l2tp');
    const warn = $('#l2tp-warn');
    const blocked = !l.capable || !l.host_network;
    if (blocked) {
      if (!l.capable) {
        warn.textContent = t('l2tp.warnText');
      } else if (!l.host_network) {
        warn.textContent = t('l2tp.warnHostNetwork');
      }
      warn.style.display = '';
      panel.querySelectorAll('.card').forEach(el => { el.style.opacity = '0.45'; el.style.pointerEvents = 'none'; });
      warn.style.pointerEvents = 'auto';
      warn.style.opacity = '1';
    } else {
      warn.style.display = 'none';
      panel.querySelectorAll('.card').forEach(el => { el.style.opacity = ''; el.style.pointerEvents = ''; });
    }
  } catch(e) {}
  // Load IKEv2 config
  await loadIKEv2();
  // Load WireGuard config
  await loadWireGuard();
}

async function saveHy2UserAuth() {
  const enabled = $('#hy2-user-auth').checked;
  try {
    await api('/node', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ hy2_user_auth: enabled }) });
    toast(enabled ? t('hy2.authEnabled') : t('hy2.authDisabled'), 'success');
  } catch (e) { toast(String(e), 'error'); }
}

async function saveSocks5() {
  const port = $('#sk5-port').value.trim();
  const enabled = $('#sk5-enabled').checked;
  if (!port) { toast(t('error.portRequired'), 'error'); return; }
  if (enabled) {
    const ok = await checkPortConflicts([{port: parseInt(port), proto: 'tcp', desc: 'SOCKS5', inputId: '#sk5-port'}]);
    if (!ok) return;
  }
  try {
    // Delete existing socks5 proxies first
    const proxies = await api('/proxies');
    for (const p of proxies) {
      if (p.protocol === 'socks5') await api(`/proxies/${p.id}`, { method: 'DELETE' });
    }
    // Add new
    await api('/proxies', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({
      id: 'socks5', protocol: 'socks5', listen: '0.0.0.0:' + port, enabled
    })});
    toast(t('socks5.saved'), 'success');
  } catch(e) { toast(String(e), 'error'); }
}

async function saveSS() {
  const port = $('#ss-port').value.trim();
  const enabled = $('#ss-enabled').checked;
  const method = $('#ss-method').value;
  if (!port) { toast(t('error.portRequired'), 'error'); return; }
  if (enabled) {
    const ok = await checkPortConflicts([{port: parseInt(port), proto: 'tcp', desc: 'Shadowsocks', inputId: '#ss-port'}]);
    if (!ok) return;
  }
  try {
    await api('/ss', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({
      listen: '0.0.0.0:' + port, enabled, method
    })});
    toast(t('ss.saved'), 'success');
  } catch(e) { toast(String(e), 'error'); }
}

async function saveL2TP() {
  const listen = $('#l2tp-port').value.trim();
  const enabled = $('#l2tp-enabled').checked;
  const pool = $('#l2tp-pool').value.trim();
  const psk = $('#l2tp-psk').value.trim();
  if (enabled && (!listen || !pool || !psk)) { toast(t('l2tp.portPoolPskRequired'), 'error'); return; }
  const port = parseInt(listen) || 1701;
  if (enabled) {
    const ok = await checkPortConflicts([
      {port, proto: 'udp', desc: 'L2TP', inputId: '#l2tp-port'},
      {port: 500, proto: 'udp', desc: 'IKE'},
      {port: 4500, proto: 'udp', desc: 'IKE NAT-T'},
    ]);
    if (!ok) return;
  }
  try {
    const mtu = parseInt($('#l2tp-mtu').value) || 1280;
    await api('/l2tp', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({ listen, enabled, pool, psk, mtu })});
    toast(t('l2tp.saved'), 'success');
  } catch(e) { toast(String(e), 'error'); }
}

// ── IKEv2/IPsec ──
function ikev2ModeChanged() {
  const mode = $('#ikev2-mode').value;
  $('#ikev2-mschapv2-fields').style.display = mode === 'mschapv2' ? '' : 'none';
  $('#ikev2-psk-fields').style.display = mode === 'psk' ? '' : 'none';
  if (mode === 'psk') checkIKEv2ExitReachability();
}

async function checkIKEv2ExitReachability() {
  const exitId = $('#ikev2-default-exit').value.trim();
  const el = $('#ikev2-exit-status');
  if (!exitId) { el.textContent = ''; return; }
  try {
    const topo = await api('/topology');
    const peer = topo.find(p => p.name === exitId);
    if (peer && peer.connected) {
      el.innerHTML = '<span style="color:var(--accent)">' + t('ikev2.reachable', {name: exitId}) + '</span>';
    } else if (peer) {
      el.innerHTML = '<span style="color:#e74c3c">' + t('ikev2.notConnected', {name: exitId}) + '</span>';
    } else {
      el.innerHTML = '<span style="color:#e74c3c">' + t('ikev2.notFound', {name: exitId}) + '</span>';
    }
  } catch(e) { el.textContent = ''; }
}

async function loadIKEv2() {
  try {
    const cfg = await api('/ikev2');
    $('#ikev2-mode').value = cfg.mode || 'mschapv2';
    $('#ikev2-enabled').checked = cfg.enabled;
    $('#ikev2-pool').value = cfg.pool || '';
    $('#ikev2-mtu').value = cfg.mtu || 1400;
    $('#ikev2-local-id').value = cfg.local_id || '';
    $('#ikev2-remote-id').value = cfg.remote_id || '';
    $('#ikev2-psk').value = cfg.psk || '';
    $('#ikev2-default-exit').value = cfg.default_exit || '';
    ikev2ModeChanged();

    // Load CA certs for dropdown (only CAs with private key)
    try {
      const certs = await api('/tls');
      const sel = $('#ikev2-cert');
      sel.innerHTML = `<option value="">${t('ikev2.selectCACert')}</option>`;
      for (const c of certs) {
        if (!c.is_ca || !c.key_file) continue;
        const opt = document.createElement('option');
        opt.value = c.id;
        opt.textContent = (c.name || c.id) + ' (CA)';
        if (c.id === cfg.cert_id) opt.selected = true;
        sel.appendChild(opt);
      }
    } catch(e) {}

    // Capability check
    const panel = $('#ptab-ikev2');
    const warn = $('#ikev2-warn');
    const blocked = !cfg.capable || !cfg.host_network;
    if (blocked) {
      if (!cfg.capable) {
        warn.textContent = t('ikev2.warnText');
      } else if (!cfg.host_network) {
        warn.textContent = t('ikev2.warnHostNetwork');
      }
      warn.style.display = '';
      panel.querySelectorAll('.card').forEach(el => { el.style.opacity = '0.45'; el.style.pointerEvents = 'none'; });
      warn.style.pointerEvents = 'auto';
      warn.style.opacity = '1';
    } else {
      warn.style.display = 'none';
      panel.querySelectorAll('.card').forEach(el => { el.style.opacity = ''; el.style.pointerEvents = ''; });
    }
  } catch(e) {}
}

async function saveIKEv2() {
  const mode = $('#ikev2-mode').value;
  const enabled = $('#ikev2-enabled').checked;
  const pool = $('#ikev2-pool').value.trim();

  if (enabled && !pool) { toast(t('ikev2.poolRequired'), 'error'); return; }

  const mtu = parseInt($('#ikev2-mtu').value) || 1400;
  const local_id = $('#ikev2-local-id').value.trim();
  const remote_id = $('#ikev2-remote-id').value.trim();
  const body = { enabled, mode, pool, mtu, local_id, remote_id };

  if (mode === 'mschapv2') {
    body.cert_id = $('#ikev2-cert').value;
    if (enabled && !body.cert_id) { toast(t('ikev2.certRequired'), 'error'); return; }
  } else {
    body.psk = $('#ikev2-psk').value.trim();
    body.default_exit = $('#ikev2-default-exit').value.trim();
    if (enabled && !body.psk) { toast(t('ikev2.pskRequired'), 'error'); return; }
  }

  if (enabled) {
    // Skip port check if L2TP is enabled (they share strongswan on 500/4500)
    const l2tpEnabled = $('#l2tp-enabled') && $('#l2tp-enabled').checked;
    if (!l2tpEnabled) {
      const ok = await checkPortConflicts([
        {port: 500, proto: 'udp', desc: 'IKE'},
        {port: 4500, proto: 'udp', desc: 'IKE NAT-T'},
      ]);
      if (!ok) return;
    }
  }
  try {
    await api('/ikev2', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
    toast(t('ikev2.saved'), 'success');
  } catch(e) { toast(String(e), 'error'); }
}

// ── WireGuard ──
let _wgPeers = [];
let editingWGPeer = null; // null = add mode, string = editing peer name

async function loadWireGuard() {
  try {
    const wg = await api('/wireguard');
    $('#wg-enabled').checked = wg.enabled;
    $('#wg-port').value = wg.listen_port || 51820;
    $('#wg-address').value = wg.address || '10.0.0.1/24';
    $('#wg-privkey').value = wg.private_key || '';
    $('#wg-pubkey').value = wg.public_key || '';
    // DNS reuses Settings page value, not shown in WG panel
    $('#wg-mtu').value = wg.mtu || 1420;
    if (wg.running) {
      const c = wg.connected || 0;
      $('#wg-status').textContent = c > 0 ? '● ' + t('wg.connectedStatus', {count: c}) : '● ' + t('wg.runningStatus');
      $('#wg-status').style.color = '#27ae60';
    } else {
      $('#wg-status').textContent = '';
      $('#wg-status').style.color = '';
    }
    _wgPeers = wg.peers || [];
    renderWGPeers();
  } catch(e) {}
}
function renderWGPeers() {
  const el = $('#wg-peer-list');
  $('#wg-peer-count').textContent = _wgPeers.length;
  if (!_wgPeers.length) {
    el.innerHTML = '<div class="empty" data-i18n="wg.noPeers" data-i18n-html>' + t('wg.noPeers') + '</div>';
    return;
  }
  el.innerHTML = `<div class="table-scroll"><table class="peer-table user-table"><thead><tr>
    <th style="width:120px">${t('wg.peerName')}</th>
    <th style="min-width:180px">${t('wg.peerExitVia')}</th>
    <th style="width:130px">${t('wg.peerAllowedIPs')}</th>
    <th style="width:50px">${t('wg.ka')}</th>
    <th style="width:150px"></th>
  </tr></thead><tbody>${_wgPeers.map(p => {
    return `<tr>
      <td><a href="#" onclick="openWGPeerDetail('${esc(p.name)}');return false" style="font-weight:600;color:var(--primary);text-decoration:none">${esc(p.name)}</a></td>
      <td><span style="font-family:var(--mono);font-size:12px">${exitViaHTML(p.exit_via)}</span>${exitModeBadge(p.exit_mode)}</td>
      <td style="font-family:var(--mono);font-size:12px">${esc(p.allowed_ips)}</td>
      <td>${p.keepalive || '-'}</td>
      <td style="text-align:right"><div class="act-group">
        <button class="act-btn edit" onclick="editWGPeer('${esc(p.name)}')">${t('app.edit')}</button>
        <button class="act-btn danger" onclick="removeWGPeer('${esc(p.name)}')">${t('app.delete')}</button>
      </div></td>
    </tr>`;
  }).join('')}</tbody></table></div>`;
}
async function generateWGServerKey() {
  try {
    const k = await api('/wireguard/generate-key', { method: 'POST' });
    $('#wg-privkey').value = k.private_key;
    $('#wg-pubkey').value = k.public_key;
  } catch(e) { toast(String(e), 'error'); }
}
function wgPrivKeyChanged() {
  const v = $('#wg-privkey').value.trim();
  if (!v) { $('#wg-pubkey').value = ''; return; }
  if (!isValidWGKey(v)) { toast(t('wg.invalidPrivKeyShort'), 'error'); return; }
  $('#wg-pubkey').value = '(save to derive)';
}
function isValidWGKey(k) {
  try { return atob(k).length === 32; } catch(e) { return false; }
}
function validateWGKey(input) {
  const v = input.value.trim();
  if (v && !isValidWGKey(v)) {
    toast(t('wg.invalidKeyLong'), 'error');
    input.style.borderColor = '#e74c3c';
  } else {
    input.style.borderColor = '';
  }
}
// Check if ports are available before enabling a proxy.
// ports: [{port, proto, desc, inputId}], returns true if all OK.
async function checkPortConflicts(ports) {
  try {
    // Clear previous errors
    ports.forEach(p => {
      if (p.inputId) {
        const el = $(p.inputId);
        if (el) { el.style.borderColor = ''; el.title = ''; }
        const err = document.getElementById('port-err-' + p.port);
        if (err) err.remove();
      }
    });
    const r = await api('/check-ports', { method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify(ports.map(p => ({port: p.port, proto: p.proto, desc: p.desc})))
    });
    const conflicts = r.conflicts || [];
    if (conflicts.length === 0) return true;
    for (const c of conflicts) {
      const match = ports.find(p => p.port === c.port && p.proto === c.proto);
      if (match && match.inputId) {
        const el = $(match.inputId);
        if (el) {
          el.style.borderColor = '#e74c3c';
          // Insert error message below input
          let errEl = document.getElementById('port-err-' + c.port);
          if (!errEl) {
            errEl = document.createElement('div');
            errEl.id = 'port-err-' + c.port;
            errEl.style.cssText = 'color:#e74c3c;font-size:12px;margin-top:2px';
            el.parentNode.appendChild(errEl);
          }
          errEl.textContent = t('port.conflict', {port: c.port, proto: c.proto});
        }
      }
      toast(t('port.conflict', {port: c.port, proto: c.proto}), 'error');
    }
    return false;
  } catch(e) { return true; /* allow if check fails */ }
}

async function saveWireGuard() {
  const privKey = $('#wg-privkey').value.trim();
  if (privKey && !isValidWGKey(privKey)) { toast(t('wg.invalidPrivKeyFormat'), 'error'); return; }
  const enabled = $('#wg-enabled').checked;
  const port = parseInt($('#wg-port').value) || 51820;
  // Check port conflicts when enabling
  if (enabled) {
    const ok = await checkPortConflicts([
      {port, proto: 'udp', desc: 'WireGuard', inputId: '#wg-port'}
    ]);
    if (!ok) return;
  }
  try {
    await api('/wireguard', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({
      enabled,
      listen_port: port,
      private_key: privKey,
      address: $('#wg-address').value,
      dns: '',
      mtu: parseInt($('#wg-mtu').value) || 1420,
    }) });
    toast(t('wg.saved'), 'success');
    loadWireGuard();
  } catch(e) { toast(String(e), 'error'); }
}
function closeWGPeerDialog() { closeModal('#wg-peer-dialog'); editingWGPeer = null; }
async function openWGPeerDialog() {
  editingWGPeer = null;
  $('#wgp-modal-title').textContent = t('wg.addPeerTitle');
  $('#wgp-submit').textContent = t('app.add');
  $('#wgp-name').value = '';
  $('#wgp-name').readOnly = false;
  if (window._wgpExitList) window._wgpExitList.set({paths: [], mode: ''});
  $('#wgp-keepalive').value = '25';
  // Auto-suggest next IP
  const addr = $('#wg-address').value;
  if (addr) {
    const base = addr.split('/')[0].split('.');
    const lastOctet = parseInt(base[3]) + _wgPeers.length + 1;
    if (lastOctet < 255) { base[3] = lastOctet; }
    $('#wgp-allowedips').value = base.join('.') + '/32';
  } else {
    $('#wgp-allowedips').value = '';
  }
  // Auto-generate keys
  try {
    const k = await api('/wireguard/generate-key', { method: 'POST' });
    $('#wgp-pubkey').value = k.public_key;
    $('#wgp-privkey').value = k.private_key;
  } catch(e) {}
  $('#wgp-pubkey').style.borderColor = '';
  $('#wgp-privkey').style.borderColor = '';
  openModal('#wg-peer-dialog');
}
function editWGPeer(name) {
  const p = _wgPeers.find(x => x.name === name);
  if (!p) return;
  editingWGPeer = name;
  $('#wgp-modal-title').textContent = t('wg.editPeerPrefix', {name});
  $('#wgp-submit').textContent = t('app.save');
  $('#wgp-name').value = p.name;
  $('#wgp-name').readOnly = false;
  $('#wgp-pubkey').value = p.public_key || '';
  $('#wgp-privkey').value = p.private_key || '';
  $('#wgp-allowedips').value = p.allowed_ips || '';
  if (window._wgpExitList) {
    const paths = p.exit_paths || (p.exit_via ? [p.exit_via] : []);
    window._wgpExitList.set({paths, mode: p.exit_mode || ''});
  }
  $('#wgp-keepalive').value = p.keepalive || 0;
  $('#wgp-pubkey').style.borderColor = '';
  $('#wgp-privkey').style.borderColor = '';
  openModal('#wg-peer-dialog');
}
async function generateWGPeerKeys() {
  try {
    const k = await api('/wireguard/generate-key', { method: 'POST' });
    $('#wgp-pubkey').value = k.public_key;
    $('#wgp-privkey').value = k.private_key;
    $('#wgp-pubkey').style.borderColor = '';
    $('#wgp-privkey').style.borderColor = '';
  } catch(e) { toast(String(e), 'error'); }
}
async function submitWGPeer() {
  const name = $('#wgp-name').value.trim();
  const pubKey = $('#wgp-pubkey').value.trim();
  const privKey = $('#wgp-privkey').value.trim();
  const allowedIPs = $('#wgp-allowedips').value.trim();
  if (!name) { toast(t('wg.nameRequired'), 'error'); return; }
  if (!pubKey || !isValidWGKey(pubKey)) { toast(t('wg.pubKeyRequired'), 'error'); return; }
  if (privKey && !isValidWGKey(privKey)) { toast(t('wg.invalidPrivKeyFormat'), 'error'); return; }
  if (!allowedIPs) { toast(t('wg.allowedIpsRequired'), 'error'); return; }
  if (window._wgpExitList && !window._wgpExitList.validate()) return;
  const wgExitData = window._wgpExitList ? window._wgpExitList.get() : {paths:[], mode:''};
  const body = { name, public_key: pubKey, private_key: privKey, allowed_ips: allowedIPs,
    exit_via: wgExitData.paths[0] || '', exit_paths: wgExitData.paths, exit_mode: wgExitData.mode,
    keepalive: parseInt($('#wgp-keepalive').value) || 0 };
  try {
    if (editingWGPeer) {
      if (name !== editingWGPeer) {
        // Name changed: delete old, create new
        await api('/wireguard/peers/' + encodeURIComponent(editingWGPeer), { method: 'DELETE' });
        await api('/wireguard/peers', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      } else {
        await api('/wireguard/peers/' + encodeURIComponent(editingWGPeer), { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      }
      toast(t('wg.peerUpdated'), 'success');
    } else {
      await api('/wireguard/peers', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      toast(t('wg.peerAdded'), 'success');
    }
    closeModal('#wg-peer-dialog');
    editingWGPeer = null;
    loadWireGuard();
  } catch(e) { toast(String(e), 'error'); }
}
async function removeWGPeer(name) {
  if (!await showConfirm(t('app.delete'), t('wg.deleteConfirm', {name}))) return;
  try {
    await api('/wireguard/peers/' + encodeURIComponent(name), { method: 'DELETE' });
    toast(t('wg.peerRemoved'), 'success');
    loadWireGuard();
  } catch(e) { toast(String(e), 'error'); }
}
function downloadWGPeerConfig(name) {
  const endpoint = location.hostname;
  const url = basePath + '/api/wireguard/peers/' + encodeURIComponent(name) + '/config?endpoint=' + encodeURIComponent(endpoint);
  fetch(url, { headers: { 'Authorization': 'Bearer ' + sessionStorage.getItem(tokenKey) } })
    .then(r => r.blob())
    .then(blob => {
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = name + '.conf';
      a.click();
      URL.revokeObjectURL(a.href);
    });
}
async function openWGPeerDetail(name) {
  const endpoint = location.hostname;
  const url = basePath + '/api/wireguard/peers/' + encodeURIComponent(name) + '/config?endpoint=' + encodeURIComponent(endpoint);
  try {
    const r = await fetch(url, { headers: { 'Authorization': 'Bearer ' + sessionStorage.getItem(tokenKey) } });
    const conf = await r.text();
    $('#wgp-detail-title').textContent = name;
    $('#wgp-detail-conf').textContent = conf;
    // Generate QR code
    const canvas = $('#wgp-qr-canvas');
    generateQR(canvas, conf);
    // Store name for download button
    canvas.dataset.name = name;
    openModal('#wg-peer-detail');
  } catch(e) { toast(String(e), 'error'); }
}
function closeWGPeerDetail() { closeModal('#wg-peer-detail'); }
function downloadFromDetail() {
  const name = $('#wgp-qr-canvas').dataset.name;
  if (name) downloadWGPeerConfig(name);
}

// Minimal QR Code generator (numeric mode, supports WireGuard config text)
// Uses the qr-creator pattern: encode text → modules → draw on canvas
function generateQR(canvas, text) {
  // Use the browser's built-in or a minimal encoder
  // For simplicity, encode as a data URI and use an image approach
  // Actually, let's use a compact QR lib inlined
  const size = 256;
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext('2d');
  ctx.fillStyle = '#fff';
  ctx.fillRect(0, 0, size, size);
  // Encode using the QR API endpoint (server-side) or use JS QR encoder
  // Server-side is cleanest — add a QR endpoint
  const img = new Image();
  img.onload = () => { ctx.drawImage(img, 0, 0, size, size); };
  // Use Google Charts QR API as fallback (works offline too if cached)
  // Better: generate server-side. Let's add a simple QR API.
  // For now, use the text-based SVG QR approach via a JS lib.
  // Inline minimal QR encoder:
  renderQRToCanvas(ctx, text, size);
}

// Minimal QR code renderer using bit-matrix encoding
// This is a simplified version that generates QR codes for alphanumeric text
function renderQRToCanvas(ctx, text, size) {
  // Use fetch to get QR from server
  const endpoint = location.hostname;
  fetch(basePath + '/api/wireguard/qr?text=' + encodeURIComponent(text), {
    headers: { 'Authorization': 'Bearer ' + sessionStorage.getItem(tokenKey) }
  }).then(r => r.blob()).then(blob => {
    const img = new Image();
    img.onload = () => {
      ctx.fillStyle = '#fff';
      ctx.fillRect(0, 0, size, size);
      ctx.drawImage(img, 0, 0, size, size);
    };
    img.src = URL.createObjectURL(blob);
  }).catch(() => {
    // Fallback: show text
    ctx.fillStyle = '#000';
    ctx.font = '11px monospace';
    const lines = text.split('\n');
    lines.forEach((line, i) => ctx.fillText(line, 4, 14 + i * 13));
  });
}

// ── Settings ──
function switchSettingsTab(tab) {
  const t = tab.dataset.settab;
  $$('[data-settab]').forEach(el => el.classList.toggle('active', el === tab));
  $$('.settings-panel').forEach(p => {
    const show = p.id === 'stab-' + t;
    p.style.display = show ? '' : 'none';
    if (show) { p.style.animation = 'none'; p.offsetHeight; p.style.animation = ''; }
  });
  loadSettings();
}

async function refreshHttpsCerts() {
  try {
    const certs = await api('/tls');
    const sel = $('#ui-https-cert');
    const cur = sel.value;
    sel.innerHTML = '<option value="">-- Select Certificate --</option>';
    for (const c of certs) {
      const opt = document.createElement('option');
      opt.value = c.id;
      opt.textContent = c.name || c.id;
      if (c.id === cur) opt.selected = true;
      sel.appendChild(opt);
    }
  } catch(e) {}
}

async function loadSettings() {
  try {
    const ui = await api('/settings/ui');
    $('#ui-listen').value = ui.listen || '';
    $('#ui-basepath').value = ui.base_path || '';
    $('#ui-dns').value = ui.dns || '8.8.8.8,1.1.1.1';
    $('#ui-https').checked = ui.force_https || false;
    $('#ui-session-timeout').value = ui.session_timeout_h || 12;
    toggleHttpsCert();
    // Load certs for HTTPS dropdown
    try {
      const certs = await api('/tls');
      const sel = $('#ui-https-cert');
      sel.innerHTML = `<option value="">${t('ikev2.selectCert')}</option>`;
      for (const c of certs) {
        const opt = document.createElement('option');
        opt.value = c.id;
        opt.textContent = c.name || c.id;
        if (c.id === ui.https_cert_id) opt.selected = true;
        sel.appendChild(opt);
      }
    } catch(e) {}
  } catch (e) {}
}

function toggleHttpsCert() {
  $('#ui-https-cert-group').style.display = $('#ui-https').checked ? '' : 'none';
}

// ── Import / Export XLSX ──
let _importTarget = '';

async function exportXLSX(target) {
  try {
    let data, filename, headers, label;
    if (target === 'nodes') {
      const clients = await api('/clients');
      headers = ['name','addrs','password','sni','insecure','conn_mode','max_tx','max_rx'];
      data = clients.map(c => ({
        name: c.name, addrs: (c.addrs && c.addrs.length ? c.addrs : [c.addr]).join('; '),
        password: c.password, sni: c.sni||'', insecure: c.insecure?'true':'false',
        conn_mode: c.conn_mode||'', max_tx: c.max_tx||0, max_rx: c.max_rx||0,
      }));
      filename = 'hy2scale-nodes.xlsx';
      label = t('nav.nodes');
    } else if (target === 'users') {
      const users = await api('/users');
      headers = ['username','password','exit_via','exit_mode','traffic_limit_gb','expiry_date','enabled'];
      data = users.map(u => ({
        username: u.username, password: u.password,
        exit_via: u.exit_via||'', exit_mode: u.exit_mode||'',
        traffic_limit_gb: u.traffic_limit ? (u.traffic_limit/1073741824).toFixed(2) : '0',
        expiry_date: u.expiry_date||'', enabled: u.enabled?'true':'false',
      }));
      filename = 'hy2scale-users.xlsx';
      label = t('nav.users');
    } else if (target.startsWith('rules-')) {
      const ruleType = target.replace('rules-','');
      const resp = await api('/rules');
      const rules = (resp.rules||[]).filter(r => r.type === ruleType);
      headers = ['id','name','targets','exit_via','exit_mode','enabled'];
      data = rules.map(r => ({
        id: r.id, name: r.name||'', targets: (r.targets||[]).join('\n'),
        exit_via: r.exit_via||'', exit_mode: r.exit_mode||'',
        enabled: r.enabled?'true':'false',
      }));
      filename = `hy2scale-rules-${ruleType}.xlsx`;
      label = t('nav.rules') + ' (' + ruleType + ')';
    } else return;

    if (!await showConfirm(t('import.export'), t('import.exportConfirm', {count: data.length, type: label}))) return;

    const ws = XLSX.utils.json_to_sheet(data, {header: headers});
    const wb = XLSX.utils.book_new();
    XLSX.utils.book_append_sheet(wb, ws, 'Data');
    XLSX.writeFile(wb, filename);
    toast(t('import.exported'), 'success');
  } catch(e) { toast(String(e), 'error'); }
}

function openImportDialog(target) {
  _importTarget = target;
  let title = t('import.title');
  if (target === 'nodes') title = t('import.importNodes');
  else if (target === 'users') title = t('import.importUsers');
  else if (target.startsWith('rules-')) title = t('import.importRules');
  $('#import-modal-title').textContent = title;
  $('#import-overwrite').checked = false;
  $('#import-file-input').value = '';
  $('#import-status').style.display = 'none';
  openModal('#import-modal');
}

function closeImportDialog() { closeModal('#import-modal'); _importTarget = ''; }

async function handleImportFile(input) {
  const file = input.files[0];
  if (!file) return;
  const statusEl = $('#import-status');
  statusEl.style.display = '';
  statusEl.style.color = 'var(--text-muted)';
  statusEl.textContent = t('import.processing');

  try {
    const ab = await file.arrayBuffer();
    const wb = XLSX.read(ab);
    const ws = wb.Sheets[wb.SheetNames[0]];
    const rows = XLSX.utils.sheet_to_json(ws);
    if (!rows.length) { statusEl.style.color = 'var(--red)'; statusEl.textContent = t('import.empty'); return; }

    const overwrite = $('#import-overwrite').checked;
    let added = 0, skipped = 0, errors = 0;

    if (_importTarget === 'nodes') {
      for (const r of rows) {
        const addrs = r.addrs ? String(r.addrs).split(/;\s*/).filter(Boolean) : (r.addr ? [String(r.addr)] : []);
        const body = {
          name: String(r.name||addrs[0]||''), addr: addrs[0]||'', addrs,
          password: String(r.password||''), sni: String(r.sni||''), insecure: String(r.insecure)!=='false',
          conn_mode: String(r.conn_mode||''), max_tx: parseInt(r.max_tx)||0, max_rx: parseInt(r.max_rx)||0,
        };
        if (!body.addr || !body.password) { errors++; continue; }
        try {
          if (overwrite) {
            try { await api(`/clients/${encodeURIComponent(body.name)}`, {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)}); } catch(e) {
              await api('/clients', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
            }
          } else {
            await api('/clients', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
          }
          added++;
        } catch(e) { skipped++; }
      }
    } else if (_importTarget === 'users') {
      for (const r of rows) {
        const body = {
          username: String(r.username||''), password: String(r.password||''),
          exit_via: String(r.exit_via||''), exit_mode: String(r.exit_mode||''),
          traffic_limit: Math.round((parseFloat(r.traffic_limit_gb)||0)*1073741824),
          expiry_date: String(r.expiry_date||''), enabled: String(r.enabled)!=='false',
        };
        if (!body.username || !body.password) { errors++; continue; }
        try {
          if (overwrite) {
            try { await api(`/users/${encodeURIComponent(body.username)}`, {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)}); } catch(e) {
              await api('/users', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
            }
          } else {
            await api('/users', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
          }
          added++;
        } catch(e) { skipped++; }
      }
    } else if (_importTarget.startsWith('rules-')) {
      const ruleType = _importTarget.replace('rules-','');
      for (const r of rows) {
        const targets = String(r.targets||'').split('\n').map(s=>s.trim()).filter(Boolean);
        const body = {
          id: String(r.id || ruleType+'-'+Date.now().toString(36)+Math.random().toString(36).slice(2,5)),
          name: String(r.name||''), type: ruleType, targets,
          exit_via: String(r.exit_via||''), exit_mode: String(r.exit_mode||''),
          enabled: String(r.enabled)!=='false',
        };
        if (!targets.length || !body.exit_via) { errors++; continue; }
        try {
          if (overwrite) {
            try { await api(`/rules/${encodeURIComponent(body.id)}`, {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)}); } catch(e) {
              await api('/rules', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
            }
          } else {
            await api('/rules', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
          }
          added++;
        } catch(e) { skipped++; }
      }
    }

    statusEl.style.color = 'var(--green)';
    statusEl.textContent = t('import.result', {added, skipped, errors});
    toast(t('import.result', {added, skipped, errors}), 'success');
    // Refresh the page
    if (_importTarget === 'nodes') { lastTopoJSON = ''; refreshTopology(); }
    else if (_importTarget === 'users') refreshUsers();
    else refreshRules();
  } catch(e) {
    statusEl.style.color = 'var(--red)';
    statusEl.textContent = t('import.failed') + ': ' + e;
    toast(t('import.failed') + ': ' + e, 'error');
  }
  input.value = '';
}

function downloadBackup() {
  const token = sessionStorage.getItem(tokenKey);
  const a = document.createElement('a');
  a.href = basePath + '/api/backup';
  // Use fetch to add auth header, then trigger download
  fetch(basePath + '/api/backup', { headers: { 'Authorization': 'Bearer ' + token } })
    .then(r => r.blob())
    .then(blob => {
      const url = URL.createObjectURL(blob);
      a.href = url;
      a.download = 'hy2scale-backup.tar';
      a.click();
      URL.revokeObjectURL(url);
      toast('Backup downloaded', 'success');
    })
    .catch(e => toast('Backup failed: ' + e, 'error'));
}

async function uploadRestore(input) {
  const file = input.files[0];
  if (!file) return;
  if (!confirm(t('settings.restoreConfirm'))) {
    input.value = '';
    return;
  }
  const statusEl = $('#backup-status');
  statusEl.style.display = '';
  statusEl.style.color = 'var(--text-muted)';
  statusEl.textContent = t('settings.restoreUploading');
  try {
    const token = sessionStorage.getItem(tokenKey);
    const resp = await fetch(basePath + '/api/restore', {
      method: 'POST',
      headers: { 'Authorization': 'Bearer ' + token },
      body: file,
    });
    if (!resp.ok) {
      const msg = await resp.text();
      throw msg;
    }
    statusEl.style.color = 'var(--green)';
    statusEl.textContent = t('settings.restoreComplete');
    toast(t('settings.restoreComplete'), 'success');
    // Wait for restart, then reload
    setTimeout(() => { location.reload(); }, 3000);
  } catch (e) {
    statusEl.style.color = 'var(--red)';
    statusEl.textContent = t('settings.restoreFailed') + ': ' + e;
    toast(t('settings.restoreFailed') + ': ' + e, 'error');
  }
  input.value = '';
}

async function saveDNS() {
  const dns = $('#ui-dns').value.trim();
  try {
    await api('/settings/ui', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ dns }) });
    toast(t('settings.dnsSaved'), 'success');
  } catch (e) { toast(String(e), 'error'); }
}
async function changePassword() {
  const cur = $('#set-cur-pass').value, newUser = $('#set-new-user').value.trim(),
    newPass = $('#set-new-pass').value, conf = $('#set-confirm-pass').value;
  $('#set-error').textContent = '';
  if (!cur) return void ($('#set-error').textContent = t('settings.passwordRequired'));
  if (newPass && newPass !== conf) return void ($('#set-error').textContent = t('error.passwordMismatch'));
  if (!newUser && !newPass) return void ($('#set-error').textContent = t('settings.enterNewCreds'));
  try {
    const curHash = await sha256(cur);
    const newPassHash = newPass ? await sha256(newPass) : '';
    await api('/settings/password', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ current_password: curHash, new_username: newUser, new_password: newPassHash }) });
    localStorage.removeItem('hy2scale_cred');
    toast(t('settings.passwordUpdated'), 'success');
    setTimeout(doLogout, 1500);
  } catch (e) { $('#set-error').textContent = String(e); }
}
async function updateUISettings() {
  const listen = $('#ui-listen').value.trim(), bp = $('#ui-basepath').value.trim();
  const forceHttps = $('#ui-https').checked;
  const httpsCertId = $('#ui-https-cert').value;
  $('#ui-error').textContent = ''; $('#ui-ok').textContent = '';
  try {
    const sessionTimeoutH = parseInt($('#ui-session-timeout').value) || 12;
    await api('/settings/ui', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      listen: listen || null, base_path: bp || null,
      force_https: forceHttps, https_cert_id: httpsCertId || null,
      session_timeout_h: sessionTimeoutH
    }) });
    toast(t('settings.saved'), 'success');
  } catch (e) { $('#ui-error').textContent = String(e); }
}

// ── Exit Via rendering with reachability colors ──
// ── Exit Via Autocomplete ──
let _exitPaths = []; // cached list of all reachable exit paths

function buildExitPaths(topo) {
  const paths = [];
  const names = new Set(); // all unique node names
  function walk(nodes, prefix) {
    for (const n of nodes) {
      if (!n.is_self) {
        const p = prefix ? prefix + '/' + n.name : n.name;
        paths.push(p);
        names.add(n.name);
        if (n.children) walk(n.children, p);
      } else if (n.children) {
        walk(n.children, '');
      }
    }
  }
  walk(topo, '');
  // Add standalone node names that only appear in paths
  for (const name of names) {
    if (!paths.includes(name)) paths.push(name);
  }
  _exitPaths = paths;
}

// ── Exit Path List Component ──
// Replaces single exit_via input with a multi-path list + mode selector.
// Usage: new ExitPathList(containerEl, id)
// API: .set({paths:[], mode:''}), .get() → {paths:[], mode:''}
class ExitPathList {
  constructor(containerEl, id) {
    this.id = id;
    this.el = containerEl;
    this.el.innerHTML = `
      <div class="exit-mode-options exit-path-mode" style="margin-bottom:8px">
        <label class="exit-mode-opt"><input type="radio" name="epm-${id}" value="" checked><span>${t('exit.modeNone')}</span></label>
        <label class="exit-mode-opt"><input type="radio" name="epm-${id}" value="stability"><span>${t('exit.modeStability')}</span></label>
        <label class="exit-mode-opt"><input type="radio" name="epm-${id}" value="speed"><span>${t('exit.modeSpeed')}</span></label>
      </div>
      <div class="exit-path-list addr-list"></div>
      <div class="addr-add-row exit-path-add" style="margin-top:6px"><span>${t('exit.addPath')}</span></div>`;
    this.listEl = this.el.querySelector('.exit-path-list');
    this.modeEl = this.el.querySelector('.exit-path-mode');
    this.el.querySelector('.exit-path-add').addEventListener('click', () => this.addRow(''));
    this.addRow('');
  }

  addRow(value) {
    const row = document.createElement('div');
    row.className = 'addr-row';
    const input = document.createElement('input');
    input.className = 'addr-ip';
    input.placeholder = 'e.g. node-name or path/to/node';
    input.value = value || '';
    input.style.flex = '1';
    const del = document.createElement('button');
    del.className = 'addr-del';
    del.tabIndex = -1;
    del.innerHTML = '&#8722;';
    del.addEventListener('click', () => { row.remove(); this.syncMode(); });
    row.appendChild(input);
    row.appendChild(del);
    this.listEl.appendChild(row);
    // Setup autocomplete on the input
    setupExitAutocomplete(input);
    this.syncMode();
  }

  syncMode() {
    const rows = this.listEl.querySelectorAll('.addr-row');
    const radios = this.modeEl.querySelectorAll('input[type=radio]');
    const directRadio = this.modeEl.querySelector('input[value=""]');
    const stabilityRadio = this.modeEl.querySelector('input[value="stability"]');
    const speedRadio = this.modeEl.querySelector('input[value="speed"]');
    // Delete button: first row can't be deleted
    rows.forEach((r, i) => { r.querySelector('.addr-del').disabled = (rows.length <= 1); });

    if (rows.length <= 1) {
      // Single path: force Direct
      directRadio.checked = true;
      radios.forEach(r => r.disabled = true);
      this.modeEl.classList.add('exit-mode-disabled');
    } else {
      // Multi path: disable Direct
      directRadio.disabled = true;
      stabilityRadio.disabled = false;
      speedRadio.disabled = false;
      this.modeEl.classList.remove('exit-mode-disabled');
      if (directRadio.checked) stabilityRadio.checked = true;
    }
  }

  set({paths, mode}) {
    this.listEl.innerHTML = '';
    const list = paths && paths.length ? paths : [''];
    for (const p of list) this.addRow(p);
    if (mode) {
      const r = this.modeEl.querySelector(`input[value="${mode}"]`);
      if (r) r.checked = true;
    }
    this.syncMode();
  }

  validate() {
    const rows = this.listEl.querySelectorAll('.addr-row');
    const seen = new Set();
    for (const r of rows) {
      const input = r.querySelector('input');
      const v = input.value.trim();
      input.style.borderColor = '';
      if (v && seen.has(v)) {
        input.style.borderColor = 'var(--red)';
        toast('Duplicate exit path: ' + v, 'error');
        return false;
      }
      if (v) seen.add(v);
    }
    return true;
  }

  get() {
    const rows = this.listEl.querySelectorAll('.addr-row');
    const paths = [];
    rows.forEach(r => {
      const v = r.querySelector('input').value.trim();
      if (v) paths.push(v);
    });
    const modeRadio = this.modeEl.querySelector('input[type=radio]:checked');
    return { paths, mode: modeRadio ? modeRadio.value : '' };
  }
}

function setupExitAutocomplete(inputEl) {
  if (inputEl._acSetup) return;
  inputEl._acSetup = true;
  const wrap = document.createElement('div');
  wrap.className = 'autocomplete-wrap';
  inputEl.parentNode.insertBefore(wrap, inputEl);
  wrap.appendChild(inputEl);

  // Clear button (same style as pw-eye)
  const inputWrap = document.createElement('div');
  inputWrap.className = 'pw-wrap';
  wrap.insertBefore(inputWrap, inputEl);
  inputWrap.appendChild(inputEl);
  const clearBtn = document.createElement('button');
  clearBtn.type = 'button';
  clearBtn.className = 'pw-eye exit-clear-btn';
  clearBtn.tabIndex = -1;
  clearBtn.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="14" height="14"><path d="M18 6L6 18"/><path d="M6 6l12 12"/></svg>';
  inputWrap.appendChild(clearBtn);
  clearBtn.addEventListener('click', () => {
    inputEl.value = '';
    inputEl.dispatchEvent(new Event('input'));
    inputEl.focus();
  });

  // Exit mode: shown but controlled by ExitPathList if present
  // Keep simple API for backward compat
  inputEl._setExitMode = function(mode) {};
  inputEl._getExitMode = function() { return ''; };

  const list = document.createElement('div');
  list.className = 'autocomplete-list';
  document.body.appendChild(list);

  let activeIdx = -1;

  function positionList() {
    const rect = inputEl.getBoundingClientRect();
    list.style.left = rect.left + 'px';
    list.style.top = rect.bottom + 'px';
    list.style.width = rect.width + 'px';
  }

  function update() {
    const val = inputEl.value.trim().toLowerCase();
    const matches = _exitPaths.filter(p => !val || p.toLowerCase().includes(val));
    if (!matches.length || (matches.length === 1 && matches[0].toLowerCase() === val)) {
      list.classList.remove('open'); return;
    }
    activeIdx = -1;
    list.innerHTML = matches.slice(0, 15).map((p, i) => {
      const parts = p.split('/');
      const last = parts.pop();
      const prefix = parts.length ? parts.join('/') + '/' : '';
      return `<div class="autocomplete-item" data-val="${p}">${prefix}<b>${last}</b></div>`;
    }).join('');
    positionList();
    list.classList.add('open');
  }

  inputEl.addEventListener('input', update);
  inputEl.addEventListener('focus', update);
  inputEl.addEventListener('blur', () => setTimeout(() => list.classList.remove('open'), 150));
  inputEl.addEventListener('keydown', e => {
    const items = list.querySelectorAll('.autocomplete-item');
    if (e.key === 'ArrowDown') { e.preventDefault(); activeIdx = Math.min(activeIdx + 1, items.length - 1); items.forEach((it, i) => it.classList.toggle('active', i === activeIdx)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); activeIdx = Math.max(activeIdx - 1, 0); items.forEach((it, i) => it.classList.toggle('active', i === activeIdx)); }
    else if (e.key === 'Enter' && activeIdx >= 0) { e.preventDefault(); inputEl.value = items[activeIdx].dataset.val; list.classList.remove('open'); inputEl.dispatchEvent(new Event('input')); }
    else if (e.key === 'Escape') { list.classList.remove('open'); }
  });
  list.addEventListener('mousedown', e => {
    const item = e.target.closest('.autocomplete-item');
    if (item) { inputEl.value = item.dataset.val; list.classList.remove('open'); inputEl.dispatchEvent(new Event('input')); }
  });
}

// ── Custom Select (replaces native <select>) ──
function setupCustomSelect(selectEl) {
  if (selectEl._csSetup) return;
  selectEl._csSetup = true;
  selectEl.style.display = 'none';

  const wrap = document.createElement('div');
  wrap.className = 'custom-select-wrap';
  selectEl.parentNode.insertBefore(wrap, selectEl);
  wrap.appendChild(selectEl);

  const btn = document.createElement('div');
  btn.className = 'custom-select-btn';
  btn.tabIndex = 0;
  wrap.appendChild(btn);

  const list = document.createElement('div');
  list.className = 'custom-select-list';
  document.body.appendChild(list);

  function getLabel() {
    const opt = selectEl.options[selectEl.selectedIndex];
    return opt ? opt.textContent : '';
  }
  btn.textContent = getLabel();

  function positionList() {
    const rect = btn.getBoundingClientRect();
    list.style.left = rect.left + 'px';
    list.style.top = rect.bottom + 'px';
    list.style.width = rect.width + 'px';
  }

  function buildItems() {
    list.innerHTML = '';
    for (const opt of selectEl.options) {
      const item = document.createElement('div');
      item.className = 'custom-select-item' + (opt.selected ? ' selected' : '');
      item.textContent = opt.textContent;
      item.dataset.val = opt.value;
      list.appendChild(item);
    }
  }

  btn.addEventListener('click', () => {
    buildItems();
    positionList();
    list.classList.toggle('open');
  });
  btn.addEventListener('blur', () => setTimeout(() => list.classList.remove('open'), 150));
  list.addEventListener('mousedown', e => {
    const item = e.target.closest('.custom-select-item');
    if (item) {
      selectEl.value = item.dataset.val;
      btn.textContent = item.textContent;
      list.classList.remove('open');
      selectEl.dispatchEvent(new Event('change'));
    }
  });

  // Sync when select changes programmatically
  selectEl.addEventListener('change', () => { btn.textContent = getLabel(); });
  // Observe option changes
  new MutationObserver(() => { btn.textContent = getLabel(); }).observe(selectEl, { childList: true, subtree: true, attributes: true });
  // Patch .value setter to keep button in sync
  const desc = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value');
  Object.defineProperty(selectEl, 'value', {
    get() { return desc.get.call(this); },
    set(v) { desc.set.call(this, v); btn.textContent = getLabel(); }
  });
}

function exitModeBadge(mode) {
  if (!mode) return '';
  if (mode === 'stability') return ' <span class="badge badge-green" style="font-size:10px">' + t('exit.modeStability') + '</span>';
  if (mode === 'speed') return ' <span class="badge badge-blue" style="font-size:10px">' + t('exit.modeSpeed') + '</span>';
  return '';
}

function exitViaHTML(path) {
  if (!path) return '<span style="color:var(--text-muted)">(direct)</span>';
  const hops = path.split('/').filter(Boolean);
  let reachable = true;
  return hops.map(hop => {
    if (!reachable) {
      return `<span style="color:var(--red);font-weight:600">${esc(hop)}</span>`;
    }
    if (connectedPeers.has(hop)) {
      return `<span style="color:var(--green);font-weight:600">${esc(hop)}</span>`;
    } else {
      reachable = false;
      return `<span style="color:var(--red);font-weight:600">${esc(hop)}</span>`;
    }
  }).join('<span style="color:var(--text-muted);margin:0 2px">/</span>');
}

// ── Users ──
let editingUserId = null;

async function refreshSessions() {
  try {
    const data = await api('/sessions');
    const devices = data.devices || [];
    $('#session-count').textContent = devices.length;
    const el = $('#session-list');
    if (!devices.length) {
      el.innerHTML = '<div class="empty" data-i18n="devices.noDevices">' + t('devices.noDevices') + '</div>';
      return;
    }
    el.innerHTML = `<div class="table-scroll"><table class="peer-table user-table"><thead><tr>
      <th style="width:100px">${t('devices.user')}</th>
      <th style="width:110px">${t('devices.ip')}</th>
      <th style="width:65px">${t('devices.proxy')}</th>
      <th style="width:40px">${t('devices.conn')}</th>
      <th style="width:110px">${t('devices.traffic')}</th>
      <th style="width:75px">${t('devices.duration')}</th>
      <th style="width:50px"></th>
    </tr></thead><tbody>${devices.map(d => {
      const dur = d.duration;
      const h = Math.floor(dur/3600), m = Math.floor((dur%3600)/60), sec = dur%60;
      const durStr = h > 0 ? h+'h'+m+'m' : m > 0 ? m+'m'+sec+'s' : sec+'s';
      const tx = fmtBytes(d.tx_bytes), rx = fmtBytes(d.rx_bytes);
      return `<tr>
        <td><b>${esc(d.username || '-')}</b></td>
        <td style="font-family:var(--mono);font-size:12px">${esc(d.remote_ip)}</td>
        <td><span style="font-size:12px;padding:1px 6px;background:var(--bg-subtle);border-radius:3px">${esc(d.protocol)}</span></td>
        <td>${d.conn_count}</td>
        <td style="font-size:12px"><span class="stat-up">${tx}</span> / <span class="stat-down">${rx}</span></td>
        <td style="font-size:12px">${durStr}</td>
        <td><button class="act-btn danger" onclick="kickDevice('${esc(d.key)}')">${t('devices.kick')}</button></td>
      </tr>`;
    }).join('')}</tbody></table></div>`;
  } catch(e) {}
}
async function kickDevice(key) {
  try { await api('/sessions/' + encodeURIComponent(key), { method: 'DELETE' }); refreshSessions(); toast(t('devices.kicked'), 'success'); }
  catch(e) { toast(String(e), 'error'); }
}

async function refreshUsers() {
  // Ensure connectedPeers is populated for exit_via reachability
  if (connectedPeers.size === 0) {
    try {
      const topo = await api('/topology');
      const s = new Set();
      (function collect(nodes) { for (const n of nodes) { if (n.connected || n.is_self || n.native || n.latency_ms > 0) s.add(n.name); if (n.children) collect(n.children); } })(topo);
      connectedPeers = s;
    } catch(e) {}
  }
  const users = await api('/users');
  const el = $('#user-list');
  $('#user-count').textContent = users?.length || 0;
  if (!users?.length) {
    el.innerHTML = '<div class="empty" data-i18n="users.noUsers" data-i18n-html>' + t('users.noUsers') + '</div>';
    return;
  }
  el.innerHTML = `<div class="table-scroll"><table class="peer-table user-table"><thead><tr>
    <th style="width:50px">${t('users.on')}</th>
    <th style="width:120px">${t('users.username')}</th>
    <th style="min-width:180px">${t('users.exitVia')}</th>
    <th class="col-right" style="width:130px">${t('users.traffic')}</th>
    <th class="col-right" style="width:90px">${t('users.expiry')}</th>
    <th style="width:150px"></th>
  </tr></thead><tbody>${users.map(u => {
    const limitGB = u.traffic_limit ? (u.traffic_limit / 1073741824).toFixed(1) + ' GB' : '∞';
    const usedGB = (u.traffic_used / 1073741824).toFixed(2) + ' GB';
    const pct = u.traffic_limit ? Math.min(100, (u.traffic_used / u.traffic_limit * 100)).toFixed(0) : 0;
    const expired = u.expiry_date && new Date(u.expiry_date) < new Date();
    const expiryText = u.expiry_date || '—';
    return `<tr class="${!u.enabled ? 'disabled' : ''}">
      <td><label class="toggle"><input type="checkbox" ${u.enabled ? 'checked' : ''} onchange="toggleUser('${esc(u.id)}',this.checked)"><span class="slider"></span></label></td>
      <td><b>${esc(u.username)}</b></td>
      <td><span style="font-family:var(--mono);font-size:12px">${exitViaHTML(u.exit_via)}</span>${exitModeBadge(u.exit_mode)}</td>
      <td class="col-right">
        <span style="font-size:12px">${usedGB} / ${limitGB}</span>
        ${u.traffic_limit ? `<div style="background:var(--border-light);height:3px;border-radius:2px;margin-top:3px"><div style="background:${pct > 90 ? 'var(--red)' : 'var(--primary)'};height:100%;width:${pct}%;border-radius:2px"></div></div>` : ''}
      </td>
      <td class="col-right"><span style="font-size:12px;${expired ? 'color:var(--red)' : ''}">${expiryText}</span></td>
      <td style="text-align:right"><div class="act-group">
        <button class="act-btn edit" onclick="editUser('${esc(u.id)}')">${t('app.edit')}</button>
        <button class="act-btn warn" onclick="resetTraffic('${esc(u.id)}')">${t('users.reset')}</button>
        <button class="act-btn danger" onclick="deleteUser('${esc(u.id)}','${esc(u.username)}')">${t('app.delete')}</button>
      </div></td>
    </tr>`;
  }).join('')}</tbody></table></div>`;
}

function openUserDialog() {
  editingUserId = null;
  $('#user-modal-title').textContent = t('users.addTitle');
  $('#user-submit').textContent = t('app.add');
  ['u-username','u-password','u-expiry'].forEach(id => $(`#${id}`).value = '');
  if (window._uExitList) window._uExitList.set({paths: [], mode: ''});
  $('#u-limit').value = '0';
  $('#u-enabled').checked = true;
  openModal('#user-modal');
}

async function editUser(id) {
  const users = (await api('/users')).filter(u => u.id === id);
  if (!users.length) return;
  const u = users[0];
  editingUserId = id;
  $('#user-modal-title').textContent = t('users.editPrefix', {name: u.username});
  $('#user-submit').textContent = t('app.save');
  $('#u-username').value = u.username;
  $('#u-password').value = u.password;
  if (window._uExitList) {
    const paths = u.exit_paths || (u.exit_via ? [u.exit_via] : []);
    window._uExitList.set({paths, mode: u.exit_mode || ''});
  }
  $('#u-limit').value = u.traffic_limit ? (u.traffic_limit / 1073741824).toFixed(1) : '0';
  $('#u-expiry').value = u.expiry_date || '';
  $('#u-enabled').checked = u.enabled;
  openModal('#user-modal');
}

function closeUserDialog() { closeModal('#user-modal'); editingUserId = null; }

async function submitUser() {
  const username = $('#u-username').value.trim();
  const password = $('#u-password').value;
  if (!username || !password) { toast(t('users.usernamePassRequired'), 'error'); return; }
  const limitGB = parseFloat($('#u-limit').value) || 0;
  if (window._uExitList && !window._uExitList.validate()) return;
  const exitData = window._uExitList ? window._uExitList.get() : {paths:[], mode:''};
  const body = {
    username, password,
    exit_via: exitData.paths[0] || '',
    exit_paths: exitData.paths,
    exit_mode: exitData.mode,
    traffic_limit: Math.round(limitGB * 1073741824),
    expiry_date: $('#u-expiry').value || '',
    enabled: $('#u-enabled').checked,
  };
  try {
    if (editingUserId) {
      body.id = editingUserId;
      await api(`/users/${editingUserId}`, { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      toast(t('users.updated', {name: username}), 'success');
    } else {
      await api('/users', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      toast(t('users.added', {name: username}), 'success');
    }
    closeUserDialog();
    refreshUsers();
  } catch(e) { toast(String(e), 'error'); }
}

async function toggleUser(id, enabled) {
  try { await api(`/users/${id}/toggle`, { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({enabled}) }); refreshUsers(); }
  catch(e) { toast(String(e), 'error'); }
}

async function resetTraffic(id) {
  if (!await showConfirm(t('users.resetTitle'), t('users.resetConfirm'))) return;
  try { await api(`/users/${id}/reset-traffic`, { method: 'PUT' }); refreshUsers(); toast(t('users.trafficReset'), 'success'); }
  catch(e) { toast(String(e), 'error'); }
}

async function deleteUser(id, name) {
  if (!await showConfirm(t('users.deleteTitle'), t('users.deleteConfirm', {name}))) return;
  try { await api(`/users/${id}`, { method: 'DELETE' }); refreshUsers(); toast(t('users.deleted', {name}), 'success'); }
  catch(e) { toast(String(e), 'error'); }
}

// ── TLS ──
function certExpired(notAfter) {
  if (!notAfter) return true;
  try { return new Date(notAfter) < new Date(); } catch(e) { return true; }
}

// ── Rules ──
let _editRuleId = null;

function switchRuleTab(tab) {
  $$('[data-ruletab]').forEach(t => t.classList.toggle('active', t === tab));
  $$('.rule-panel').forEach(p => {
    const show = p.id === 'ruletab-' + tab.dataset.ruletab;
    p.style.display = show ? '' : 'none';
    if (show) { p.style.animation = 'none'; p.offsetHeight; p.style.animation = ''; }
  });
}

async function refreshRules() {
  try {
    const data = await api('/rules');
    if (!data.available) {
      $('#rules-unavailable').style.display = '';
      $('#rules-content').style.display = 'none';
      // Hide nav item if not available
      return;
    }
    $('#rules-unavailable').style.display = 'none';
    $('#rules-content').style.display = '';
    const rules = data.rules || [];
    renderRuleList('ip', rules.filter(r => r.type === 'ip'));
    renderRuleList('domain', rules.filter(r => r.type === 'domain'));
  } catch(e) { console.error(e); }
}

function renderRuleList(type, rules) {
  const el = $('#' + type + '-rules-list');
  const countEl = $('#' + type + '-rule-count');
  if (countEl) countEl.textContent = rules.length;
  if (!rules.length) {
    el.innerHTML = `<div class="empty">${t('rules.noRules')}</div>`;
    return;
  }
  const targetLabel = type === 'ip' ? t('rules.ipTargets') : t('rules.domainTargets');
  el.innerHTML = `<div class="table-scroll"><table class="peer-table user-table"><thead><tr>
    <th style="width:50px">${t('users.on')}</th>
    <th style="width:140px">${t('rules.name')}</th>
    <th style="min-width:180px">${targetLabel}</th>
    <th style="min-width:120px">${t('rules.exitVia')}</th>
    <th style="width:150px"></th>
  </tr></thead><tbody>${rules.map(r => {
    const targets = r.targets || [];
    const first = esc(targets[0] || '');
    const restList = targets.slice(1).map(t => esc(t)).join('<br>');
    const more = targets.length > 1 ? ` <span class="badge badge-muted rule-more-badge" style="cursor:default" data-tip="${esc(targets.slice(1).join('\n'))}">+${targets.length - 1}</span>` : '';
    const nameDisplay = r.name ? esc(r.name) : `<span style="color:var(--text-muted)">${esc(r.id)}</span>`;
    return `<tr class="${!r.enabled ? 'disabled' : ''}">
      <td><label class="toggle"><input type="checkbox" ${r.enabled ? 'checked' : ''} onchange="toggleRuleEnabled('${esc(r.id)}',this.checked)"><span class="slider"></span></label></td>
      <td><b>${nameDisplay}</b></td>
      <td><span style="font-family:var(--mono);font-size:12px">${first}</span>${more}</td>
      <td><span style="font-family:var(--mono);font-size:12px">${exitViaHTML(r.exit_via)}</span>${exitModeBadge(r.exit_mode)}</td>
      <td style="text-align:right"><div class="act-group">
        <button class="act-btn edit" onclick="editRule('${esc(r.id)}')">${t('app.edit')}</button>
        <button class="act-btn danger" onclick="deleteRuleConfirm('${esc(r.id)}')">${t('app.delete')}</button>
      </div></td>
    </tr>`;
  }).join('')}</tbody></table></div>`;
}

let _editRuleEnabled;
function openRuleDialog(type) {
  _editRuleId = null;
  _editRuleEnabled = true;
  $('#rule-type').value = type;
  $('#rule-name').value = '';
  $('#rule-targets').value = '';
  if (window._ruleExitList) window._ruleExitList.set({paths: [], mode: ''});
  $('#rule-targets-label').textContent = type === 'ip' ? t('rules.ipTargets') : t('rules.domainTargets');
  $('#rule-targets').placeholder = type === 'ip' ? '1.1.1.1\n10.0.0.0/8\n192.168.1.1-192.168.1.254' : 'google.com\n*.github.com';
  $('#rule-modal-title').textContent = t('rules.newRule');
  $('#rule-submit-btn').textContent = t('app.add');
  openModal('#rule-modal');
}

function closeRuleDialog() { closeModal('#rule-modal'); }

async function editRule(id) {
  const data = await api('/rules');
  const rule = (data.rules || []).find(r => r.id === id);
  if (!rule) return;
  _editRuleId = id;
  _editRuleEnabled = rule.enabled;
  $('#rule-type').value = rule.type;
  $('#rule-name').value = rule.name || '';
  $('#rule-targets').value = (rule.targets || []).join('\n');
  if (window._ruleExitList) {
    const paths = rule.exit_paths || (rule.exit_via ? [rule.exit_via] : []);
    window._ruleExitList.set({paths, mode: rule.exit_mode || ''});
  }
  $('#rule-targets-label').textContent = rule.type === 'ip' ? t('rules.ipTargets') : t('rules.domainTargets');
  $('#rule-modal-title').textContent = t('app.edit');
  $('#rule-submit-btn').textContent = t('app.save');
  openModal('#rule-modal');
}

async function submitRule() {
  const type = $('#rule-type').value;
  const name = $('#rule-name').value.trim();
  const targets = $('#rule-targets').value.trim().split('\n').map(s => s.trim()).filter(Boolean);
  if (window._ruleExitList && !window._ruleExitList.validate()) return;
  const ruleExitData = window._ruleExitList ? window._ruleExitList.get() : {paths:[], mode:''};
  const exit_via = ruleExitData.paths[0] || '';
  if (!targets.length) { toast(t('rules.targetsRequired'), 'error'); return; }
  if (!exit_via) { toast(t('rules.exitRequired'), 'error'); return; }
  const id = _editRuleId || (type + '-' + Date.now().toString(36));
  const exit_mode = ruleExitData.mode;
  const body = { id, name, type, targets, exit_via, exit_paths: ruleExitData.paths, exit_mode, enabled: _editRuleEnabled !== undefined ? _editRuleEnabled : true };
  try {
    if (_editRuleId) {
      await api('/rules/' + _editRuleId, { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
    } else {
      await api('/rules', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
    }
    closeRuleDialog(); refreshRules(); toast(t('rules.saved'), 'success');
  } catch(e) { toast(String(e), 'error'); }
}

async function toggleRuleEnabled(id, enabled) {
  try {
    await api('/rules/' + id + '/toggle', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({ enabled }) });
    refreshRules();
  } catch(e) { toast(String(e), 'error'); }
}

async function deleteRuleConfirm(id) {
  if (!confirm(t('rules.deleteConfirm'))) return;
  try {
    await api('/rules/' + id, { method: 'DELETE' });
    refreshRules(); toast(t('rules.deleted'), 'success');
  } catch(e) { toast(String(e), 'error'); }
}

// ── TLS ──
async function refreshCerts() {
  const certs = await api('/tls');
  const el = $('#cert-list');
  $('#cert-count').textContent = certs?.length || 0;
  if (!certs?.length) {
    el.innerHTML = '<div class="empty" data-i18n="tls.noCerts" data-i18n-html>' + t('tls.noCerts') + '</div>';
    return;
  }
  el.innerHTML = `<div class="table-scroll"><table class="peer-table"><thead><tr>
    <th>${t('tls.name')}</th><th>${t('tls.subject')}</th><th>${t('tls.issuer')}</th><th>${t('tls.expires')}</th><th>${t('tls.hasKey')}</th><th></th>
  </tr></thead><tbody>${certs.map(c => {
    const expired = certExpired(c.not_after);
    const rowStyle = expired ? 'opacity:0.45' : '';
    return `<tr style="${rowStyle}">
    <td><b>${esc(c.name)}</b><span class="peer-addr-sub">${esc(c.id)}</span>${expired ? ` <span class="badge badge-muted">${t('tls.expired')}</span>` : ''}</td>
    <td>${esc(c.subject)}</td>
    <td>${esc(c.issuer)}${c.is_ca ? ' <span class="badge badge-blue">CA</span>' : ''}</td>
    <td><span style="font-family:var(--mono);font-size:12px">${esc(c.not_after)}</span></td>
    <td>${c.key_file ? `<span class="badge badge-green">${t('app.yes')}</span>` : `<span class="badge badge-muted">${t('app.no')}</span>`}</td>
    <td style="text-align:right;white-space:nowrap"><button class="act-btn edit" onclick="editCert('${esc(c.id)}')">${t('app.edit')}</button> <button class="act-btn danger" onclick="deleteCert('${esc(c.id)}')">${t('app.delete')}</button></td>
  </tr>`;}).join('')}</tbody></table></div>`;
}

function setupPEMDragDrop(textarea, type) {
  if (!textarea) return;
  textarea.addEventListener('dragover', e => { e.preventDefault(); textarea.classList.add('drag-over'); });
  textarea.addEventListener('dragleave', () => textarea.classList.remove('drag-over'));
  textarea.addEventListener('drop', e => {
    e.preventDefault();
    textarea.classList.remove('drag-over');
    const file = e.dataTransfer?.files?.[0];
    if (!file) return;
    // Check file extension
    const ext = file.name.split('.').pop().toLowerCase();
    const validExts = ['pem', 'crt', 'cer', 'key', 'pub', 'txt'];
    if (!validExts.includes(ext)) {
      toast(`Invalid file type ".${ext}". Expected: ${validExts.join(', ')}`, 'error');
      return;
    }
    if (file.size > 65536) {
      toast('File too large (max 64KB)', 'error');
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      const content = reader.result.trim();
      if (type === 'cert') {
        if (!content.includes('-----BEGIN CERTIFICATE') && !content.includes('-----BEGIN TRUSTED')) {
          toast('File does not appear to be a valid PEM certificate', 'error');
          return;
        }
      } else if (type === 'key') {
        if (!content.includes('-----BEGIN') || !content.includes('PRIVATE KEY')) {
          toast('File does not appear to be a valid PEM private key', 'error');
          return;
        }
      }
      textarea.value = content;
    };
    reader.onerror = () => toast('Failed to read file', 'error');
    reader.readAsText(file);
  });
}

async function openNewCertDialog() {
  ['cert-id','cert-name','cert-pem','cert-key-pem','cert-path','cert-key-path','cert-ca-cn'].forEach(x => { const e = $(`#${x}`); if (e) e.value = ''; });
  $('#cert-id').disabled = false;
  openModal('#new-cert-modal');
  $('#cert-submit-btn').textContent = t('tls.new');
  // Populate CA select with CA certs that have private keys
  const sel = $('#cert-ca-select');
  sel.innerHTML = '<option value="">\u2014 ' + t('tls.noneSelfSigned') + ' \u2014</option>';
  try {
    const certs = await api('/tls');
    (certs || []).filter(c => c.is_ca && c.key_file).forEach(c => {
      sel.innerHTML += `<option value="${esc(c.id)}">${esc(c.name)} (${esc(c.subject)})</option>`;
    });
  } catch(e) {}
  sel.onchange = onCaSelectChange;
  sel.value = '';
  $('#cert-ca-cn-group').style.display = 'none';
  $('#cert-ca-group').style.display = '';
  switchCertTab(document.querySelector('[data-certtab="paste"]'));
}
function closeNewCertDialog() { closeModal('#new-cert-modal'); }

function onCaSelectChange() {
  const hasCa = $('#cert-ca-select').value !== '';
  $('#cert-ca-cn-group').style.display = hasCa ? '' : 'none';
  // When CA selected, hide manual input (cert will be auto-generated)
  $('#cert-tab-paste').style.display = hasCa ? 'none' : '';
  $('#cert-tab-path').style.display = 'none';
  // Hide/show tab bar and gen button based on CA mode
  $$('[data-certtab]').forEach(el => el.style.display = hasCa ? 'none' : '');
  $('#cert-gen-icon').style.display = hasCa ? 'none' : '';
}

function switchCertTab(tab) {
  if (!tab) return;
  const t = tab.dataset.certtab;
  $$('[data-certtab]').forEach(el => el.classList.toggle('active', el === tab));
  if ($('#cert-ca-select').value) return; // CA mode hides tabs
  $('#cert-tab-paste').style.display = t === 'paste' ? '' : 'none';
  $('#cert-tab-path').style.display = t === 'path' ? '' : 'none';
  $('#cert-gen-icon').style.display = t === 'paste' ? '' : 'none';
}

async function generateCertPEM() {
  const id = $('#cert-id').value.trim();
  if (!id) { toast(t('tls.fillIdFirst'), 'error'); return; }
  // If CA is selected, use CA signing instead of self-signed
  const caId = $('#cert-ca-select') ? $('#cert-ca-select').value : '';
  if (caId) {
    const cn = ($('#cert-ca-cn') ? $('#cert-ca-cn').value.trim() : '') || id;
    try {
      await api('/tls/sign', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ca_id: caId, id, name: $('#cert-name').value.trim() || id, cn, days: 7300 }) });
      const pem = await api(`/tls/${id}/pem`);
      if (pem.cert) $('#cert-pem').value = pem.cert;
      if (pem.key) $('#cert-key-pem').value = pem.key;
      toast(t('tls.certSignedByCA'), 'success');
    } catch (e) { toast(String(e), 'error'); }
    return;
  }
  try {
    await api('/tls/generate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name: id, domains: [id], days: 3650 }) });
    const pem = await api(`/tls/${id}/pem`);
    if (pem.cert) $('#cert-pem').value = pem.cert;
    if (pem.key) $('#cert-key-pem').value = pem.key;
    toast(t('tls.certGenerated'), 'success');
  } catch (e) { toast(String(e), 'error'); }
}

async function submitCertDialog() {
  const id = $('#cert-id').value.trim(), name = $('#cert-name').value.trim();
  if (!id) { toast(t('tls.idRequired'), 'error'); return; }

  // CA signing mode
  const caId = $('#cert-ca-select') ? $('#cert-ca-select').value : '';
  if (caId) {
    const cn = ($('#cert-ca-cn') ? $('#cert-ca-cn').value.trim() : '') || id;
    try {
      await api('/tls/sign', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ca_id: caId, id, name: name || cn, cn, days: 7300 }) });
      closeNewCertDialog(); refreshCerts(); toast(t('tls.certSignedByCA'), 'success');
    } catch (e) { toast(String(e), 'error'); }
    return;
  }

  const pasteTab = $('#cert-tab-paste').style.display !== 'none';
  if (pasteTab) {
    const cert = $('#cert-pem').value.trim(), key = $('#cert-key-pem').value.trim();
    if (!cert) { toast(t('tls.certPemRequired'), 'error'); return; }
    try {
      await api('/tls/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name: name || id, cert, key }) });
      closeNewCertDialog(); refreshCerts(); toast(t('tls.certSaved'), 'success');
    } catch (e) { toast(String(e), 'error'); }
  } else {
    const certPath = $('#cert-path').value.trim(), keyPath = $('#cert-key-path').value.trim();
    if (!certPath) { toast(t('tls.certPathRequired'), 'error'); return; }
    try {
      await api('/tls/import-path', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name: name || id, cert_path: certPath, key_path: keyPath }) });
      closeNewCertDialog(); refreshCerts(); toast(t('tls.certSaved'), 'success');
    } catch (e) { toast(String(e), 'error'); }
  }
}

async function editCert(id) {
  ['cert-id','cert-name','cert-pem','cert-key-pem','cert-path','cert-key-path'].forEach(x => { const e = $(`#${x}`); if (e) e.value = ''; });
  $('#cert-id').value = id;
  $('#cert-id').disabled = true;
  try {
    const pem = await api(`/tls/${id}/pem`);
    if (pem.cert) $('#cert-pem').value = pem.cert;
    if (pem.key) $('#cert-key-pem').value = pem.key;
  } catch(e) {}
  openModal('#new-cert-modal');
  $('#cert-submit-btn').textContent = t('app.save');
  $('#cert-ca-group').style.display = 'none'; // Hide CA options in edit mode
  $('#cert-ca-cn-group').style.display = 'none';
  switchCertTab(document.querySelector('[data-certtab="paste"]'));
}

async function deleteCert(id) {
  if (!await showConfirm(t('tls.deleteTitle'), t('tls.deleteConfirm', {id}))) return;
  try { await api(`/tls/${id}`, { method: 'DELETE' }); refreshCerts(); toast(t('tls.deleted', {id}), 'success'); } catch (e) { toast(String(e), 'error'); }
}

// ── Language Switcher ──
function updateLangButtons() {
  const name = I18N.available.find(l => l.code === I18N.lang)?.name || I18N.lang;
  document.querySelectorAll('.lang-text').forEach(el => { el.textContent = name; });
}
function toggleLangMenu(btn) {
  const switcher = (btn || document.getElementById('lang-btn')).closest('.lang-switcher');
  const isOpen = switcher.classList.contains('open');
  // Close all
  document.querySelectorAll('.lang-switcher').forEach(s => s.classList.remove('open'));
  if (!isOpen) {
    const menu = switcher.querySelector('.lang-menu');
    menu.innerHTML = I18N.available.map(l =>
      `<div class="lang-menu-item ${l.code === I18N.lang ? 'active' : ''}" onclick="switchLang('${l.code}')">${l.name}</div>`
    ).join('');
    switcher.classList.add('open');
  }
}
async function switchLang(code) {
  await I18N.load(code);
  document.querySelectorAll('.lang-switcher').forEach(s => s.classList.remove('open'));
  updateLangButtons();
  if (pageTitles[_currentPage]) {
    $('#page-title').textContent = t(pageTitles[_currentPage]);
  }
  try { switchPage(_currentPage, false); } catch(e) {}
  if (_currentPage === 'nodes') lastTopoJSON = '';
  if (_currentPage === 'proxies') proxiesLoaded = false;
}
document.addEventListener('click', e => {
  if (!e.target.closest('.lang-switcher')) {
    document.querySelectorAll('.lang-switcher').forEach(s => s.classList.remove('open'));
  }
});

// Universal floating tooltip for badges
(function() {
  const tip = document.createElement('div');
  tip.className = 'float-tip';
  document.body.appendChild(tip);
  let hideTimer = null;

  function show(badge) {
    clearTimeout(hideTimer);
    if (badge.dataset.tip) {
      tip.textContent = badge.dataset.tip;
    } else if (badge.dataset.ipstatus) {
      try {
        const statuses = JSON.parse(badge.dataset.ipstatus);
        const avgLat = parseInt(badge.dataset.lat) || 0;
        tip.innerHTML = statuses.map(s => {
          const statusColors = {
            online: 'var(--green)',
            offline: 'var(--red)',
            mismatch: '#f59e0b',
            native: '#8b5cf6)'
          };
          const statusLabels = {
            online: avgLat > 0 ? avgLat + 'ms' : 'online',
            offline: 'offline',
            mismatch: 'mismatch',
            native: 'native'
          };
          const color = statusColors[s.status] || 'var(--text-muted)';
          const label = statusLabels[s.status] || s.status;
          const latStyle = s.status === 'online' && avgLat > 0
            ? (avgLat < 80 ? 'color:var(--green)' : avgLat < 200 ? 'color:#f59e0b' : 'color:var(--red)')
            : 'color:' + color;
          return `<div style="display:flex;justify-content:space-between;gap:20px"><span style="color:${s.status === 'online' ? 'var(--text)' : color}">${s.addr}</span><span style="${latStyle};font-weight:600;font-size:11px">${label}</span></div>`;
        }).join('');
      } catch(e) { return; }
    } else return;
    const rect = badge.getBoundingClientRect();
    tip.style.left = rect.left + 'px';
    tip.style.top = (rect.bottom + 4) + 'px';
    tip.style.transformOrigin = 'top left';
    tip.classList.add('visible');
  }

  function hide() {
    hideTimer = setTimeout(() => tip.classList.remove('visible'), 100);
  }

  document.addEventListener('mouseover', e => {
    const badge = e.target.closest('[data-tip],[data-ipstatus]');
    if (badge) show(badge);
    else hide();
  });
})();

// ── Init ──
(async function init() {
  // Load i18n
  await I18N.load(I18N.lang);
  updateLangButtons();

  // No auto-login: user must click Sign In every time
  // Session token in sessionStorage persists only within the browser tab
  routeFromURL();
  if (sessionStorage.getItem(tokenKey) && location.pathname.replace(basePath, '').replace(/^\/+/, '') !== 'login') {
    refresh();
  }
  // Setup PEM drag-drop on cert textareas
  setupPEMDragDrop($('#cert-pem'), 'cert');
  setupPEMDragDrop($('#cert-key-pem'), 'key');
  // Setup exit path list components
  if ($('#u-exitvia-list')) window._uExitList = new ExitPathList($('#u-exitvia-list'), 'u');
  if ($('#wgp-exitvia-list')) window._wgpExitList = new ExitPathList($('#wgp-exitvia-list'), 'wgp');
  if ($('#rule-exit-list')) window._ruleExitList = new ExitPathList($('#rule-exit-list'), 'rule');
  // IKEv2 default exit: keep as simple input with autocomplete
  const ikExit = $('#ikev2-default-exit');
  if (ikExit) setupExitAutocomplete(ikExit);
  // Setup custom selects for all <select> elements
  $$('select').forEach(sel => setupCustomSelect(sel));
})();
