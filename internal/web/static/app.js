const $ = s => document.querySelector(s);
const $$ = s => document.querySelectorAll(s);
const basePath = window.__BASE__ || '';
const tokenKey = 'token:' + basePath;
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
    $('#confirm-modal').style.display = '';
    const ok = () => { cleanup(); resolve(true); };
    const cancel = () => { cleanup(); resolve(false); };
    const cleanup = () => {
      $('#confirm-modal').style.display = 'none';
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
    if (r.status === 401) { doLogout(); throw 'session expired'; }
    if (!r.ok) return r.text().then(t => { throw t });
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
const pageTitles = { nodes: 'Nodes', users: 'Users', proxies: 'Proxies', tls: 'TLS', settings: 'Settings' };

function switchPage(name, push) {
  if (!pageTitles[name]) name = 'nodes';
  closeSidebar();
  $$('.nav-item[data-page]').forEach(n => n.classList.toggle('active', n.dataset.page === name));
  $$('.page').forEach(p => p.style.display = 'none');
  $(`#page-${name}`).style.display = '';
  $('#page-title').textContent = pageTitles[name];
  if (push !== false) history.pushState(null, '', basePath + '/' + name);
  if (name === 'users') refreshUsers();
  if (name === 'proxies') refreshProxies();
  if (name === 'settings') loadSettings();
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
    $('#login-pass').value = saved.p;
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
    const r = await fetch(basePath + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
    if (!r.ok) { $('#login-error').textContent = 'Invalid username or password'; return; }
    sessionStorage.setItem(tokenKey, (await r.json()).token);
    // Remember credentials
    if ($('#login-remember').checked) {
      localStorage.setItem('hy2scale_cred', JSON.stringify({ u: username, p: password }));
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
    if (node.version) {
      const vb = $('#version-badge');
      if (node.limited) {
        vb.textContent = 'v' + node.version + ' Limited';
        vb.classList.add('limited');
      } else {
        vb.textContent = 'v' + node.version;
        vb.classList.remove('limited');
      }
    }
    const tasks = [refreshTopology(), refreshStats()];
    if (!proxiesLoaded) { tasks.push(refreshProxies().catch(()=>{})); proxiesLoaded = true; }
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
  if (syncingNodes.has(name)) return '<span class="latency latency-sync">syncing</span>';
  if (ms === -1) return '<span class="latency latency-off">offline</span>';
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
      <td class="col-status"><span class="latency latency-good">∞ms</span></td>
      <td class="col-dir">${dirHTML('local')}</td>
      <td class="col-name">
        <span class="peer-name-cell">${esc(n.name)}</span>
        ${n.addr ? `<span class="peer-addr-sub">${esc(n.addr)} (UDP)</span>` : '<span class="peer-addr-sub">no hy2 server</span>'}
      </td>
      <td class="col-traffic">${trafficHTML(0, 0)}</td>
      <td class="col-nested"><label class="toggle toggle-disabled"><input type="checkbox" checked disabled><span class="slider"></span></label></td>
      <td class="col-actions"><div class="act-group">
        <button class="act-btn edit" onclick="openEditSelf()">Edit</button>
        <button class="act-btn ${n.disabled ? 'enable' : 'warn'}" onclick="toggleSelfDisable(${!n.disabled})">${n.disabled ? 'Enable' : 'Disable'}</button>
      </div></td>
    </tr>`;
  }

  const chain = n.native ? [] : [n.name];
  const nativeBadge = n.native ? ' <span class="badge badge-muted">NATIVE</span>' : '';
  const syncing = syncingNodes.has(n.name);
  const syncData = syncingNodes.get(n.name);
  const nestedChecked = syncing ? syncData.enabled : n.nested;
  const nested = n.native
    ? '<label class="toggle toggle-disabled"><input type="checkbox" disabled><span class="slider"></span></label>'
    : (n.direction === 'outbound'
      ? `<label class="toggle"><input type="checkbox" ${nestedChecked ? 'checked' : ''} onchange="toggleNested('${esc(n.name)}',this.checked)"><span class="slider"></span></label>`
      : '');
  const actions = n.direction === 'outbound' ? `<div class="act-group">
    <button class="act-btn edit" onclick="openEditDialog('${esc(n.name)}')">Edit</button>
    <button class="act-btn ${n.disabled ? 'enable' : 'warn'}" onclick="toggleDisable('${esc(n.name)}',${!n.disabled})">${n.disabled ? 'Enable' : 'Disable'}</button>
    <button class="act-btn danger" onclick="removeClient('${esc(n.name)}')">Delete</button>
  </div>` : '';

  return `<tr class="${n.disabled ? 'disabled' : ''} ${syncing ? 'syncing' : ''}">
    <td class="col-status">${latencyHTML(n.latency_ms, n.name)}</td>
    <td class="col-dir">${dirHTML(n.direction)}</td>
    <td class="col-name">
      ${n.native ? `<span class="peer-name-cell peer-rename" onclick="renameNative('${esc(n.name)}')">${esc(n.name)}</span>` : nameLink(n.name, chain)}${nativeBadge}
      ${n.addr ? `<span class="peer-addr-sub">${esc(n.addr)}</span>` : ''}
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
  const cSyncing = syncingNodes.has(c.name);
  const cSyncData = syncingNodes.get(c.name);
  const cNestedChecked = cSyncing ? cSyncData.enabled : c.nested;
  const nestedToggle = c.native
    ? '<label class="toggle toggle-disabled"><input type="checkbox" disabled><span class="slider"></span></label>'
    : `<label class="toggle"><input type="checkbox" ${cNestedChecked ? 'checked' : ''} onchange="toggleNested('${esc(c.name)}',this.checked)"><span class="slider"></span></label>`;
  const nameCell = c.native
    ? `<span class="peer-name-cell">${esc(c.name)}</span>`
    : nameLink(c.name, chain);
  const actions = `<button class="act-btn ${dis ? 'enable' : 'warn'}" onclick="toggleNestedDisable('${esc(c.via)}','${esc(c.name)}',${!dis})">${dis ? 'Enable' : 'Disable'}</button>`;

  // Build guide lines for ancestor depths + current branch
  let treeHTML = '';
  for (let d = 0; d < depth - 1; d++) {
    treeHTML += `<span class="tree-guide${guides[d] ? ' tree-guide-active' : ''}" aria-hidden="true"></span>`;
  }
  treeHTML += `<span class="tree-branch${isLast ? ' tree-last' : ''}" aria-hidden="true"></span>`;

  let html = `<tr class="sub-row${dis ? ' disabled' : ''}${cSyncing ? ' syncing' : ''}">
    <td class="col-status">${dis ? latencyHTML(-1, c.name) : latencyHTML(c.latency_ms, c.name)}</td>
    <td class="col-dir">${dir}</td>
    <td class="col-name">
      ${treeHTML}<span class="sub-name-wrap">
        ${nameCell}${nativeBadge}
        <span class="peer-addr-sub">via ${esc(c.via)}</span>
      </span>
    </td>
    <td class="col-traffic">${trafficHTML(c.tx_rate, c.rx_rate)}</td>
    <td class="col-nested">${nestedToggle}</td>
    <td class="col-actions"><div class="act-group">${actions}</div></td>
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
    let k = n.name + '|' + n.connected + '|' + n.nested + '|' + n.disabled + '|' + (n.native||'');
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
  function collectPeers(nodes) {
    for (const n of nodes) {
      if (n.connected || n.is_self || n.native || n.latency_ms > 0) newConnected.add(n.name);
      if (n.children) collectPeers(n.children);
    }
  }
  collectPeers(topo);
  connectedPeers = newConnected;
  buildExitPaths(topo);

  // Cache latencies and clear syncing for nodes with children
  for (const n of topo) {
  }

  const el = $('#topology-tree');
  if (!topo?.length) {
    el.innerHTML = '<div class="empty">No connections. Click <b>+ Add Node</b> to connect.</div>';
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
      <th class="col-status">Status</th>
      <th class="col-dir">Dir</th>
      <th class="col-name">Node</th>
      <th class="col-traffic">Traffic</th>
      <th class="col-nested">Nested</th>
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
    await api(`/peers/${name}/nested`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }) });
    // Poll until topology reflects the actual change
    const poll = setInterval(async () => {
      try {
        const topo = await api('/topology');
        const node = topo.find(n => n.name === name);
        if (!node) return;
        // Check if the nested flag in topology matches what we requested
        if (node.nested === enabled) {
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
  $('#confirm-title').textContent = 'Rename Node';
  $('#confirm-ok').style.background = 'var(--primary)';
  $('#confirm-ok').style.borderColor = 'var(--primary)';
  $('#confirm-ok').textContent = 'Save';
  $('#confirm-modal').style.display = '';
  input.focus();
  input.select();
  const ok = await new Promise(resolve => {
    const done = (v) => { $('#confirm-modal').style.display = 'none'; $('#confirm-ok').style.background = ''; $('#confirm-ok').style.borderColor = ''; $('#confirm-ok').textContent = 'Confirm'; resolve(v); };
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
    toast(`Renamed to ${newName}`, 'success');
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
  if (!await showConfirm('Delete Node', `Remove connection to "${name}"?`)) return;
  try { await api(`/clients/${name}`, { method: 'DELETE' }); lastTopoJSON = ''; refreshTopology(); toast(`Deleted ${name}`, 'success'); }
  catch (e) { toast(String(e), 'error'); }
}

// ── Node Modal (Add / Edit) ──
let editingNode = null; // null = add mode, string = edit mode (name)

function openAddDialog() {
  editingNode = null;
  $('#add-node-modal-title').textContent = 'Add Node Connection';
  $('#add-node-submit').textContent = 'Connect';
  ['add-addr','add-pass','add-sni','add-ca','add-tx','add-rx','add-isw','add-msw','add-icw','add-mcw'].forEach(id => $(`#${id}`).value = '');
  $('#add-addr').disabled = false;
  $('#add-insecure').checked = true; $('#add-fastopen').checked = false;
  $('#add-node-modal').style.display = '';
  $('#quic-advanced').style.display = 'none';
}

async function openEditDialog(name) {
  try {
    const cl = await api(`/clients/${encodeURIComponent(name)}`);
    editingNode = name;
    $('#add-node-modal-title').textContent = `Edit: ${name}`;
    $('#add-node-submit').textContent = 'Save';
    $('#add-addr').value = cl.addr || ''; $('#add-addr').disabled = true;
    $('#add-pass').value = cl.password || '';
    $('#add-sni').value = cl.sni || '';
    $('#add-insecure').checked = cl.insecure !== false;
    $('#add-ca').value = cl.ca || '';
    $('#add-tx').value = cl.max_tx ? (cl.max_tx / 125000).toFixed(0) : '';
    $('#add-rx').value = cl.max_rx ? (cl.max_rx / 125000).toFixed(0) : '';
    $('#add-isw').value = cl.init_stream_window || '';
    $('#add-msw').value = cl.max_stream_window || '';
    $('#add-icw').value = cl.init_conn_window || '';
    $('#add-mcw').value = cl.max_conn_window || '';
    $('#add-fastopen').checked = !!cl.fast_open;
    $('#add-node-modal').style.display = '';
    const hasQuic = cl.init_stream_window || cl.max_stream_window || cl.init_conn_window || cl.max_conn_window;
    $('#quic-advanced').style.display = hasQuic ? '' : 'none';
  } catch (e) { toast(String(e), 'error'); }
}

function closeAddDialog() { $('#add-node-modal').style.display = 'none'; editingNode = null; }

async function submitAddNode() {
  const addr = $('#add-addr').value.trim(), password = $('#add-pass').value.trim();
  if (!addr || !password) { toast('Address and password are required', 'error'); return; }
  const name = editingNode || addr; // use addr as temp name; remote ID replaces it after connect
  const body = {
    name, addr, password,
    sni: $('#add-sni').value.trim(), insecure: $('#add-insecure').checked, ca: $('#add-ca').value.trim(),
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
    toast(wasEdit ? `Updated ${name}` : `Connected to ${name}`, 'success');
    lastTopoJSON = ''; setTimeout(refreshTopology, 1000);
  } catch (e) { toast(String(e), 'error'); }
}

// ── Edit Self Modal ──
async function openEditSelf() {
  try {
    const [n, certs] = await Promise.all([api('/node'), api('/tls')]);
    $('#self-nodeid').value = n.node_id || '';
    $('#self-srv-listen').value = n.server?.listen || '';
    $('#self-srv-pass').value = n.server?.password || '';
    // Populate TLS cert dropdown
    const sel = $('#self-srv-tls');
    const currentCert = n.server?.tls_cert || '';
    sel.innerHTML = '<option value="">Self-signed (auto)</option>';
    if (certs?.length) {
      for (const c of certs) {
        if (!c.key_file) continue; // need private key
        const selected = c.cert_file === currentCert ? ' selected' : '';
        sel.innerHTML += `<option value="${esc(c.id)}"${selected}>${esc(c.name)} (${esc(c.subject)})</option>`;
      }
    }
    $('#self-error').textContent = '';
    $('#self-ok').textContent = '';
    $('#edit-self-modal').style.display = '';
  } catch (e) { toast(String(e), 'error'); }
}
function closeEditSelf() { $('#edit-self-modal').style.display = 'none'; }

async function submitEditSelf() {
  $('#self-error').textContent = ''; $('#self-ok').textContent = '';
  const nodeId = $('#self-nodeid').value.trim();
  const srvListen = $('#self-srv-listen').value.trim();
  const srvPass = $('#self-srv-pass').value.trim();
  const tlsId = $('#self-srv-tls').value;

  if (!nodeId) { $('#self-error').textContent = 'Node ID is required'; return; }

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
    toast('Node settings saved. Server changes require restart.', 'success');
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
  $$('.proxy-panel').forEach(p => p.classList.toggle('active', p.id === 'ptab-' + tab.dataset.ptab));
  $$('.proxy-panel').forEach(p => p.style.display = p.classList.contains('active') ? '' : 'none');
  // Reload data for the active tab to clear unsaved changes
  refreshProxies();
}

async function refreshIKEv2Certs() {
  try {
    const certs = await api('/tls');
    const sel = $('#ikev2-cert');
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
        warn.textContent = 'Insufficient privileges — container requires --cap-add NET_ADMIN and --network host to enable L2TP.';
      } else if (!l.host_network) {
        warn.textContent = 'L2TP requires --network host. Docker port mapping cannot handle IPsec transport mode. Deploy with: docker run --network host --cap-add NET_ADMIN ...';
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
}

async function saveHy2UserAuth() {
  const enabled = $('#hy2-user-auth').checked;
  try {
    await api('/node', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ hy2_user_auth: enabled }) });
    toast('Hysteria user auth ' + (enabled ? 'enabled' : 'disabled'), 'success');
  } catch (e) { toast(String(e), 'error'); }
}

async function saveSocks5() {
  const port = $('#sk5-port').value.trim();
  const enabled = $('#sk5-enabled').checked;
  if (!port) { toast('Port required', 'error'); return; }
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
    toast('SOCKS5 saved', 'success');
  } catch(e) { toast(String(e), 'error'); }
}

async function saveSS() {
  const port = $('#ss-port').value.trim();
  const enabled = $('#ss-enabled').checked;
  const method = $('#ss-method').value;
  if (!port) { toast('Port required', 'error'); return; }
  try {
    await api('/ss', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({
      listen: '0.0.0.0:' + port, enabled, method
    })});
    toast('Shadowsocks saved', 'success');
  } catch(e) { toast(String(e), 'error'); }
}

async function saveL2TP() {
  const listen = $('#l2tp-port').value.trim();
  const enabled = $('#l2tp-enabled').checked;
  const pool = $('#l2tp-pool').value.trim();
  const psk = $('#l2tp-psk').value.trim();
  if (enabled && (!listen || !pool || !psk)) { toast('Port, pool and PSK required', 'error'); return; }
  try {
    const mtu = parseInt($('#l2tp-mtu').value) || 1280;
    await api('/l2tp', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify({ listen, enabled, pool, psk, mtu })});
    toast('L2TP saved. Restart required for L2TP changes.', 'success');
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
      el.innerHTML = '<span style="color:var(--accent)">&#x2713; ' + exitId + ' reachable</span>';
    } else if (peer) {
      el.innerHTML = '<span style="color:#e74c3c">&#x2717; ' + exitId + ' not connected</span>';
    } else {
      el.innerHTML = '<span style="color:#e74c3c">&#x2717; ' + exitId + ' not found</span>';
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

    // Load certs for dropdown
    try {
      const certs = await api('/tls');
      const sel = $('#ikev2-cert');
      sel.innerHTML = '<option value="">-- Select Certificate --</option>';
      for (const c of certs) {
        const opt = document.createElement('option');
        opt.value = c.id;
        opt.textContent = c.name || c.id;
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
        warn.textContent = 'Insufficient privileges — container requires --cap-add NET_ADMIN and --network host to enable IKEv2/IPsec.';
      } else if (!cfg.host_network) {
        warn.textContent = 'IKEv2/IPsec requires --network host. Docker port mapping cannot handle IPsec tunnel mode. Deploy with: docker run --network host --cap-add NET_ADMIN ...';
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

  if (enabled && !pool) { toast('Address pool required', 'error'); return; }

  const mtu = parseInt($('#ikev2-mtu').value) || 1400;
  const local_id = $('#ikev2-local-id').value.trim();
  const remote_id = $('#ikev2-remote-id').value.trim();
  const body = { enabled, mode, pool, mtu, local_id, remote_id };

  if (mode === 'mschapv2') {
    body.cert_id = $('#ikev2-cert').value;
    if (enabled && !body.cert_id) { toast('Certificate required for MSCHAPv2 mode', 'error'); return; }
  } else {
    body.psk = $('#ikev2-psk').value.trim();
    body.default_exit = $('#ikev2-default-exit').value.trim();
    if (enabled && !body.psk) { toast('Pre-shared key required', 'error'); return; }
  }

  try {
    await api('/ikev2', { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
    toast('IKEv2 saved. Restart required for changes.', 'success');
  } catch(e) { toast(String(e), 'error'); }
}

// ── Settings ──
function switchSettingsTab(tab) {
  const t = tab.dataset.settab;
  $$('[data-settab]').forEach(el => el.classList.toggle('active', el === tab));
  $$('.settings-panel').forEach(p => p.style.display = p.id === 'stab-' + t ? '' : 'none');
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
    toggleHttpsCert();
    // Load certs for HTTPS dropdown
    try {
      const certs = await api('/tls');
      const sel = $('#ui-https-cert');
      sel.innerHTML = '<option value="">-- Select Certificate --</option>';
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

async function saveDNS() {
  const dns = $('#ui-dns').value.trim();
  try {
    await api('/settings/ui', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ dns }) });
    toast('DNS saved. Restart required.', 'success');
  } catch (e) { toast(String(e), 'error'); }
}
async function changePassword() {
  const cur = $('#set-cur-pass').value, newUser = $('#set-new-user').value.trim(),
    newPass = $('#set-new-pass').value, conf = $('#set-confirm-pass').value;
  $('#set-error').textContent = '';
  if (!cur) return void ($('#set-error').textContent = 'Current password is required');
  if (newPass && newPass !== conf) return void ($('#set-error').textContent = 'Passwords do not match');
  if (!newUser && !newPass) return void ($('#set-error').textContent = 'Enter a new username or password');
  try {
    await api('/settings/password', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ current_password: cur, new_username: newUser, new_password: newPass }) });
    localStorage.removeItem('hy2scale_cred');
    toast('Password updated. Redirecting to login...', 'success');
    setTimeout(doLogout, 1500);
  } catch (e) { $('#set-error').textContent = String(e); }
}
async function updateUISettings() {
  const listen = $('#ui-listen').value.trim(), bp = $('#ui-basepath').value.trim();
  const forceHttps = $('#ui-https').checked;
  const httpsCertId = $('#ui-https-cert').value;
  $('#ui-error').textContent = ''; $('#ui-ok').textContent = '';
  try {
    await api('/settings/ui', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({
      listen: listen || null, base_path: bp || null,
      force_https: forceHttps, https_cert_id: httpsCertId || null
    }) });
    toast('Saved. Restart container to apply.', 'success');
  } catch (e) { $('#ui-error').textContent = String(e); }
}

// ── Exit Via rendering with reachability colors ──
// ── Exit Via Autocomplete ──
let _exitPaths = []; // cached list of all reachable exit paths

function buildExitPaths(topo) {
  const paths = [];
  function walk(nodes, prefix) {
    for (const n of nodes) {
      if (n.is_self) continue;
      const p = prefix ? prefix + '/' + n.name : n.name;
      paths.push(p);
      if (n.children) walk(n.children, p);
    }
  }
  walk(topo, '');
  _exitPaths = paths;
}

function setupExitAutocomplete(inputEl) {
  if (inputEl._acSetup) return;
  inputEl._acSetup = true;
  const wrap = document.createElement('div');
  wrap.className = 'autocomplete-wrap';
  inputEl.parentNode.insertBefore(wrap, inputEl);
  wrap.appendChild(inputEl);
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
    el.innerHTML = '<div class="empty">No users. Click <b>+ Add User</b> to create one.</div>';
    return;
  }
  el.innerHTML = `<div class="table-scroll"><table class="peer-table user-table"><thead><tr>
    <th style="width:50px">On</th>
    <th style="width:120px">Username</th>
    <th style="min-width:180px">Exit Via</th>
    <th class="col-right" style="width:130px">Traffic</th>
    <th class="col-right" style="width:90px">Expiry</th>
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
      <td><span style="font-family:var(--mono);font-size:12px">${exitViaHTML(u.exit_via)}</span></td>
      <td class="col-right">
        <span style="font-size:12px">${usedGB} / ${limitGB}</span>
        ${u.traffic_limit ? `<div style="background:var(--border-light);height:3px;border-radius:2px;margin-top:3px"><div style="background:${pct > 90 ? 'var(--red)' : 'var(--primary)'};height:100%;width:${pct}%;border-radius:2px"></div></div>` : ''}
      </td>
      <td class="col-right"><span style="font-size:12px;${expired ? 'color:var(--red)' : ''}">${expiryText}</span></td>
      <td style="text-align:right"><div class="act-group">
        <button class="act-btn edit" onclick="editUser('${esc(u.id)}')">Edit</button>
        <button class="act-btn warn" onclick="resetTraffic('${esc(u.id)}')">Reset</button>
        <button class="act-btn danger" onclick="deleteUser('${esc(u.id)}','${esc(u.username)}')">Delete</button>
      </div></td>
    </tr>`;
  }).join('')}</tbody></table></div>`;
}

function openUserDialog() {
  editingUserId = null;
  $('#user-modal-title').textContent = 'Add User';
  $('#user-submit').textContent = 'Add';
  ['u-username','u-password','u-exitvia','u-expiry'].forEach(id => $(`#${id}`).value = '');
  $('#u-limit').value = '0';
  $('#u-enabled').checked = true;
  $('#user-modal').style.display = '';
}

async function editUser(id) {
  const users = (await api('/users')).filter(u => u.id === id);
  if (!users.length) return;
  const u = users[0];
  editingUserId = id;
  $('#user-modal-title').textContent = `Edit: ${u.username}`;
  $('#user-submit').textContent = 'Save';
  $('#u-username').value = u.username;
  $('#u-password').value = u.password;
  $('#u-exitvia').value = u.exit_via || '';
  $('#u-limit').value = u.traffic_limit ? (u.traffic_limit / 1073741824).toFixed(1) : '0';
  $('#u-expiry').value = u.expiry_date || '';
  $('#u-enabled').checked = u.enabled;
  $('#user-modal').style.display = '';
}

function closeUserDialog() { $('#user-modal').style.display = 'none'; editingUserId = null; }

async function submitUser() {
  const username = $('#u-username').value.trim();
  const password = $('#u-password').value;
  if (!username || !password) { toast('Username and password required', 'error'); return; }
  const limitGB = parseFloat($('#u-limit').value) || 0;
  const body = {
    username, password,
    exit_via: $('#u-exitvia').value.trim(),
    traffic_limit: Math.round(limitGB * 1073741824),
    expiry_date: $('#u-expiry').value || '',
    enabled: $('#u-enabled').checked,
  };
  try {
    if (editingUserId) {
      body.id = editingUserId;
      await api(`/users/${editingUserId}`, { method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      toast(`Updated ${username}`, 'success');
    } else {
      await api('/users', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(body) });
      toast(`Added ${username}`, 'success');
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
  if (!await showConfirm('Reset Traffic', 'Reset traffic counter to 0?')) return;
  try { await api(`/users/${id}/reset-traffic`, { method: 'PUT' }); refreshUsers(); toast('Traffic reset', 'success'); }
  catch(e) { toast(String(e), 'error'); }
}

async function deleteUser(id, name) {
  if (!await showConfirm('Delete User', `Delete user "${name}"?`)) return;
  try { await api(`/users/${id}`, { method: 'DELETE' }); refreshUsers(); toast(`Deleted ${name}`, 'success'); }
  catch(e) { toast(String(e), 'error'); }
}

// ── TLS ──
function certExpired(notAfter) {
  if (!notAfter) return true;
  try { return new Date(notAfter) < new Date(); } catch(e) { return true; }
}

async function refreshCerts() {
  const certs = await api('/tls');
  const el = $('#cert-list');
  $('#cert-count').textContent = certs?.length || 0;
  if (!certs?.length) {
    el.innerHTML = '<div class="empty">No certificates. Click <b>New</b> to create or import one.</div>';
    return;
  }
  el.innerHTML = `<div class="table-scroll"><table class="peer-table"><thead><tr>
    <th>Name</th><th>Subject</th><th>Issuer</th><th>Expires</th><th>Key</th><th></th>
  </tr></thead><tbody>${certs.map(c => {
    const expired = certExpired(c.not_after);
    const rowStyle = expired ? 'opacity:0.45' : '';
    return `<tr style="${rowStyle}">
    <td><b>${esc(c.name)}</b><span class="peer-addr-sub">${esc(c.id)}</span>${expired ? ' <span class="badge badge-muted">expired</span>' : ''}</td>
    <td>${esc(c.subject)}</td>
    <td>${esc(c.issuer)}${c.is_ca ? ' <span class="badge badge-blue">CA</span>' : ''}</td>
    <td><span style="font-family:var(--mono);font-size:12px">${esc(c.not_after)}</span></td>
    <td>${c.key_file ? '<span class="badge badge-green">yes</span>' : '<span class="badge badge-muted">no</span>'}</td>
    <td style="text-align:right;white-space:nowrap"><button class="act-btn edit" onclick="editCert('${esc(c.id)}')">Edit</button> <button class="act-btn danger" onclick="deleteCert('${esc(c.id)}')">Delete</button></td>
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

function openNewCertDialog() {
  // Clear all fields
  ['cert-id','cert-name','cert-pem','cert-key-pem','cert-path','cert-key-path'].forEach(x => { const e = $(`#${x}`); if (e) e.value = ''; });
  $('#cert-id').disabled = false;
  $('#new-cert-modal').style.display = '';
  $('#cert-submit-btn').textContent = 'New';
  switchCertTab(document.querySelector('[data-certtab="paste"]'));
}
function closeNewCertDialog() { $('#new-cert-modal').style.display = 'none'; }

function switchCertTab(tab) {
  const t = tab.dataset.certtab;
  $$('[data-certtab]').forEach(el => el.classList.toggle('active', el === tab));
  $('#cert-tab-paste').style.display = t === 'paste' ? '' : 'none';
  $('#cert-tab-path').style.display = t === 'path' ? '' : 'none';
  $('#cert-gen-icon').style.display = t === 'paste' ? '' : 'none';
}

async function generateCertPEM() {
  const id = $('#cert-id').value.trim();
  if (!id) { toast('Fill in ID first', 'error'); return; }
  try {
    await api('/tls/generate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name: id, domains: [id], days: 3650 }) });
    // Fetch the generated PEM and fill into fields
    const pem = await api(`/tls/${id}/pem`);
    if (pem.cert) $('#cert-pem').value = pem.cert;
    if (pem.key) $('#cert-key-pem').value = pem.key;
    toast('Certificate generated, review and save', 'success');
  } catch (e) { toast(String(e), 'error'); }
}

async function submitCertDialog() {
  const id = $('#cert-id').value.trim(), name = $('#cert-name').value.trim();
  if (!id) { toast('ID is required', 'error'); return; }

  const pasteTab = $('#cert-tab-paste').style.display !== 'none';
  if (pasteTab) {
    const cert = $('#cert-pem').value.trim(), key = $('#cert-key-pem').value.trim();
    if (!cert) { toast('Certificate PEM required', 'error'); return; }
    try {
      await api('/tls/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name: name || id, cert, key }) });
      closeNewCertDialog(); refreshCerts(); toast('Certificate saved', 'success');
    } catch (e) { toast(String(e), 'error'); }
  } else {
    const certPath = $('#cert-path').value.trim(), keyPath = $('#cert-key-path').value.trim();
    if (!certPath) { toast('Certificate file path required', 'error'); return; }
    try {
      await api('/tls/import-path', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name: name || id, cert_path: certPath, key_path: keyPath }) });
      closeNewCertDialog(); refreshCerts(); toast('Certificate saved', 'success');
    } catch (e) { toast(String(e), 'error'); }
  }
}

async function editCert(id) {
  // Clear and pre-fill
  ['cert-id','cert-name','cert-pem','cert-key-pem','cert-path','cert-key-path'].forEach(x => { const e = $(`#${x}`); if (e) e.value = ''; });
  $('#cert-id').value = id;
  $('#cert-id').disabled = true;
  try {
    const pem = await api(`/tls/${id}/pem`);
    if (pem.cert) $('#cert-pem').value = pem.cert;
    if (pem.key) $('#cert-key-pem').value = pem.key;
  } catch(e) {}
  $('#new-cert-modal').style.display = '';
  $('#cert-submit-btn').textContent = 'Save';
  switchCertTab(document.querySelector('[data-certtab="paste"]'));
}

async function deleteCert(id) {
  if (!await showConfirm('Delete Certificate', `Delete certificate "${id}"?`)) return;
  try { await api(`/tls/${id}`, { method: 'DELETE' }); refreshCerts(); toast(`Deleted ${id}`, 'success'); } catch (e) { toast(String(e), 'error'); }
}

// ── Init ──
(async function init() {
  if (!sessionStorage.getItem(tokenKey)) {
    // Try auto-login
    let loggedIn = false;

    if (window.__PROXY__) {
      // Proxy mode: try local node's saved credentials first
      const saved = JSON.parse(localStorage.getItem('hy2scale_cred') || 'null');
      if (saved) {
        try {
          const r = await fetch(basePath + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: saved.u, password: saved.p }) });
          if (r.ok) { sessionStorage.setItem(tokenKey, (await r.json()).token); loggedIn = true; }
        } catch (e) {}
      }
    } else {
      // Local mode: try remembered credentials
      const saved = JSON.parse(localStorage.getItem('hy2scale_cred') || 'null');
      if (saved) {
        try {
          const r = await fetch(basePath + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: saved.u, password: saved.p }) });
          if (r.ok) { sessionStorage.setItem(tokenKey, (await r.json()).token); loggedIn = true; }
          else { localStorage.removeItem('hy2scale_cred'); }
        } catch (e) {}
      }
    }
  }
  routeFromURL();
  if (sessionStorage.getItem(tokenKey) && location.pathname.replace(basePath, '').replace(/^\/+/, '') !== 'login') {
    refresh();
  }
  // Setup PEM drag-drop on cert textareas
  setupPEMDragDrop($('#cert-pem'), 'cert');
  setupPEMDragDrop($('#cert-key-pem'), 'key');
  // Setup exit via autocomplete
  const uExit = $('#u-exitvia');
  if (uExit) setupExitAutocomplete(uExit);
  const ikExit = $('#ikev2-default-exit');
  if (ikExit) setupExitAutocomplete(ikExit);
  // Setup custom selects for all <select> elements
  $$('select').forEach(sel => setupCustomSelect(sel));
})();
