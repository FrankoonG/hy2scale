const $ = s => document.querySelector(s);
const $$ = s => document.querySelectorAll(s);
const basePath = window.__BASE__ || '';

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
  const token = sessionStorage.getItem('token');
  if (token) headers['Authorization'] = 'Bearer ' + token;
  return fetch(basePath + '/api' + path, { ...opts, headers }).then(r => {
    if (r.status === 401) { doLogout(); throw 'session expired'; }
    if (!r.ok) return r.text().then(t => { throw t });
    return r.json();
  });
}

function esc(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

// ── Navigation / Router ──
const pageTitles = { nodes: 'Nodes', proxies: 'Proxies', tls: 'TLS', settings: 'Settings' };

function switchPage(name, push) {
  if (!pageTitles[name]) name = 'nodes';
  $$('.nav-item[data-page]').forEach(n => n.classList.toggle('active', n.dataset.page === name));
  $$('.page').forEach(p => p.style.display = 'none');
  $(`#page-${name}`).style.display = '';
  $('#page-title').textContent = pageTitles[name];
  if (push !== false) history.pushState(null, '', basePath + '/' + name);
  if (name === 'settings') loadSettings();
  if (name === 'tls') refreshCerts();
}

function routeFromURL() {
  const path = location.pathname.replace(basePath, '').replace(/^\/+/, '');
  const seg = path.split('/')[0];
  if (seg === 'login' || !sessionStorage.getItem('token')) {
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
    sessionStorage.setItem('token', (await r.json()).token);
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
  sessionStorage.removeItem('token');
  clearInterval(pollTimer);
  showLogin();
}
$('#login-pass').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
$('#login-user').addEventListener('keydown', e => { if (e.key === 'Enter') $('#login-pass').focus(); });

// ── Polling ──
let pollTimer = null, lastTopoJSON = '';

async function refresh() {
  try {
    const node = await api('/node');
    $('#node-badge').textContent = node.node_id;
    $('#node-name-display').textContent = node.name !== node.node_id ? node.name : '';
    await Promise.all([refreshTopology(), refreshProxies(), refreshStats()]);
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
function latencyHTML(ms) {
  if (ms === -2) return '<span class="latency latency-conflict">conflict</span>';
  if (ms === -1) return '<span class="latency latency-off">offline</span>';
  if (ms === 0) return '<span class="latency latency-na">—</span>';
  const cls = ms < 80 ? 'latency-good' : ms < 200 ? 'latency-med' : 'latency-bad';
  return `<span class="latency ${cls}">${ms}ms</span>`;
}

function dirHTML(dir) {
  if (dir === 'local') return '<span class="badge badge-green">LOCAL</span>';
  return dir === 'inbound'
    ? '<span class="badge badge-blue">IN</span>'
    : '<span class="badge badge-orange">OUT</span>';
}

function remoteURL(chain) {
  return basePath + '/remote/' + chain.map(encodeURIComponent).join('/');
}

function nameLink(name, chain) {
  if (!chain || !chain.length) return `<span class="peer-name-cell">${esc(name)}</span>`;
  return `<a class="peer-name-cell peer-link" href="${remoteURL(chain)}" target="_blank">${esc(name)}</a>`;
}

function parentRowHTML(n) {
  if (n.is_self) {
    return `<tr class="self-row${n.disabled ? ' disabled' : ''}">
      <td class="col-latency"><span class="latency latency-good">∞ms</span></td>
      <td class="col-dir">${dirHTML('local')}</td>
      <td class="col-name">
        <span class="peer-name-cell">${esc(n.name)}</span>
        ${n.addr ? `<span class="peer-addr-sub">${esc(n.addr)} (UDP)</span>` : '<span class="peer-addr-sub">no hy2 server</span>'}
      </td>
      <td class="col-nested"></td>
      <td class="col-actions"><div class="act-group">
        <button class="act-btn edit" onclick="openEditSelf()">Edit</button>
        <button class="act-btn ${n.disabled ? 'enable' : 'warn'}" onclick="toggleSelfDisable(${!n.disabled})">${n.disabled ? 'Enable' : 'Disable'}</button>
      </div></td>
    </tr>`;
  }

  const chain = [n.name];
  const nested = n.direction === 'outbound'
    ? `<label class="toggle"><input type="checkbox" ${n.nested ? 'checked' : ''} onchange="toggleNested('${esc(n.name)}',this.checked)"><span class="slider"></span></label>`
    : '';
  const actions = n.direction === 'outbound' ? `<div class="act-group">
    <button class="act-btn edit" onclick="openEditDialog('${esc(n.name)}')">Edit</button>
    <button class="act-btn ${n.disabled ? 'enable' : 'warn'}" onclick="toggleDisable('${esc(n.name)}',${!n.disabled})">${n.disabled ? 'Enable' : 'Disable'}</button>
    <button class="act-btn danger" onclick="removeClient('${esc(n.name)}')">Delete</button>
  </div>` : '';

  return `<tr class="${n.disabled ? 'disabled' : ''}">
    <td class="col-latency">${latencyHTML(n.latency_ms)}</td>
    <td class="col-dir">${dirHTML(n.direction)}</td>
    <td class="col-name">
      ${nameLink(n.name, chain)}
      ${n.addr ? `<span class="peer-addr-sub">${esc(n.addr)}</span>` : ''}
    </td>
    <td class="col-nested">${nested}</td>
    <td class="col-actions">${actions}</td>
  </tr>`;
}

function childRowHTML(c, isLast, depth, parentChain) {
  depth = depth || 1;
  parentChain = parentChain || [];
  const chain = [...parentChain, c.name];
  const conflict = !!c.conflict;
  const dis = conflict || isNestedDisabled(c.via, c.name);
  const conflictBadge = conflict ? ' <span class="badge badge-red">ID CONFLICT</span>' : '';
  const dir = c.direction ? dirHTML(c.direction) : '';
  const indent = 'padding-left:' + (depth * 20) + 'px';
  const nestedToggle = conflict ? '' : `<label class="toggle"><input type="checkbox" ${c.nested ? 'checked' : ''} onchange="toggleNested('${esc(c.name)}',this.checked)"><span class="slider"></span></label>`;
  const actions = conflict ? '' : `<button class="act-btn ${dis ? 'enable' : 'warn'}" onclick="toggleNestedDisable('${esc(c.via)}','${esc(c.name)}',${!dis})">${dis ? 'Enable' : 'Disable'}</button>`;

  let html = `<tr class="sub-row${dis ? ' disabled' : ''}${conflict ? ' conflict' : ''}">
    <td class="col-latency">${conflict ? latencyHTML(-2) : (dis ? latencyHTML(-1) : latencyHTML(c.latency_ms))}</td>
    <td class="col-dir">${dir}</td>
    <td class="col-name" style="${indent}">
      <span class="tree-branch" aria-hidden="true">${isLast ? '└' : '├'}</span><span class="sub-name-wrap">
        ${conflict ? `<span class="peer-name-cell">${esc(c.name)}</span>` : nameLink(c.name, chain)}${conflictBadge}
        <span class="peer-addr-sub">via ${esc(c.via)}</span>
      </span>
    </td>
    <td class="col-nested">${nestedToggle}</td>
    <td class="col-actions"><div class="act-group">${actions}</div></td>
  </tr>`;

  // Render grandchildren recursively
  if (c.children?.length) {
    for (let i = 0; i < c.children.length; i++) {
      html += childRowHTML(c.children[i], i === c.children.length - 1, depth + 1, chain);
    }
  }
  return html;
}

async function refreshTopology() {
  const topo = await api('/topology');
  const json = JSON.stringify(topo);
  if (json === lastTopoJSON) return;
  lastTopoJSON = json;

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
        rows += childRowHTML(n.children[i], i === n.children.length - 1, 1, parentChain);
      }
    }
  }

  el.innerHTML = `<table class="peer-table">
    <thead><tr>
      <th class="col-latency">Latency</th>
      <th class="col-dir">Dir</th>
      <th class="col-name">Node</th>
      <th class="col-nested">Nested</th>
      <th class="col-actions"></th>
    </tr></thead>
    <tbody>${rows}</tbody>
  </table>`;
  $('#peer-count').textContent = count;
}

async function toggleNested(name, enabled) {
  try { await api(`/peers/${name}/nested`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }) }); lastTopoJSON = ''; refreshTopology(); }
  catch (e) { toast(String(e), 'error'); }
}

async function toggleDisable(name, disabled) {
  try { await api(`/clients/${name}/disable`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ disabled }) }); lastTopoJSON = ''; refreshTopology(); }
  catch (e) { toast(String(e), 'error'); }
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
  $('#add-name').value = ''; $('#add-name').disabled = false;
  ['add-addr','add-pass','add-sni','add-ca','add-tx','add-rx','add-isw','add-msw','add-icw','add-mcw'].forEach(id => $(`#${id}`).value = '');
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
    $('#add-name').value = cl.name; $('#add-name').disabled = true;
    $('#add-addr').value = cl.addr || '';
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
    // Show QUIC section if any values are set
    const hasQuic = cl.init_stream_window || cl.max_stream_window || cl.init_conn_window || cl.max_conn_window;
    $('#quic-advanced').style.display = hasQuic ? '' : 'none';
  } catch (e) { toast(String(e), 'error'); }
}

function closeAddDialog() { $('#add-node-modal').style.display = 'none'; editingNode = null; }

async function submitAddNode() {
  const name = $('#add-name').value.trim(), addr = $('#add-addr').value.trim(), password = $('#add-pass').value.trim();
  if (!name || !addr || !password) { toast('Name, address and password are required', 'error'); return; }
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
    $('#self-ok').textContent = 'Saved. Server changes require restart.';
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
async function refreshProxies() {
  const proxies = await api('/proxies');
  const el = $('#proxy-list');
  $('#proxy-count').textContent = proxies?.length || 0;
  if (!proxies?.length) { el.innerHTML = '<div class="empty">No proxies. Click <b>+ Add Proxy</b> to create one.</div>'; return; }
  el.innerHTML = proxies.map(p => `
    <div class="proxy-row">
      <span class="proxy-tag">${esc(p.protocol)}</span>
      <span class="proxy-listen">${esc(p.listen)}</span>
      <span class="proxy-arrow">&rarr;</span>
      <span class="proxy-exit">${esc(p.exit_via) || '(local exit)'}</span>
      <span class="spacer"></span>
      <span class="badge badge-muted">${esc(p.id)}</span>
      <button class="btn-icon" onclick="removeProxy('${esc(p.id)}')" title="Remove">&#10005;</button>
    </div>`).join('');
}
function openProxyDialog() { $('#add-proxy-modal').style.display = ''; }
function closeProxyDialog() { $('#add-proxy-modal').style.display = 'none'; }
async function submitAddProxy() {
  const id = $('#px-id').value.trim(), protocol = $('#px-proto').value,
    listen = $('#px-listen').value.trim(), exit_via = $('#px-exit').value.trim();
  if (!id || !listen) { toast('ID and listen address are required', 'error'); return; }
  try {
    await api('/proxies', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, protocol, listen, exit_via }) });
    closeProxyDialog(); ['px-id','px-listen','px-exit'].forEach(id => $(`#${id}`).value = ''); refreshProxies();
  } catch (e) { toast(String(e), 'error'); }
}
async function removeProxy(id) {
  if (!await showConfirm('Delete Proxy', `Remove proxy "${id}"?`)) return;
  try { await api(`/proxies/${id}`, { method: 'DELETE' }); refreshProxies(); toast(`Removed ${id}`, 'success'); } catch (e) { toast(String(e), 'error'); }
}

// ── Settings ──
async function loadSettings() {
  try {
    const ui = await api('/settings/ui');
    $('#ui-listen').value = ui.listen || ''; $('#ui-basepath').value = ui.base_path || '';
  } catch (e) {}
}
async function changePassword() {
  const cur = $('#set-cur-pass').value, newUser = $('#set-new-user').value.trim(),
    newPass = $('#set-new-pass').value, conf = $('#set-confirm-pass').value;
  $('#set-error').textContent = ''; $('#set-ok').textContent = '';
  if (!cur) return void ($('#set-error').textContent = 'Current password is required');
  if (newPass && newPass !== conf) return void ($('#set-error').textContent = 'Passwords do not match');
  if (!newUser && !newPass) return void ($('#set-error').textContent = 'Enter a new username or password');
  try {
    await api('/settings/password', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ current_password: cur, new_username: newUser, new_password: newPass }) });
    ['set-cur-pass','set-new-user','set-new-pass','set-confirm-pass'].forEach(id => $(`#${id}`).value = '');
    localStorage.removeItem('hy2scale_cred');
    $('#set-ok').textContent = 'Updated. Redirecting to login...'; setTimeout(doLogout, 1500);
  } catch (e) { $('#set-error').textContent = String(e); }
}
async function updateUISettings() {
  const listen = $('#ui-listen').value.trim(), bp = $('#ui-basepath').value.trim();
  $('#ui-error').textContent = ''; $('#ui-ok').textContent = '';
  try {
    await api('/settings/ui', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ listen: listen || null, base_path: bp || null }) });
    $('#ui-ok').textContent = 'Saved. Restart the container to apply.';
  } catch (e) { $('#ui-error').textContent = String(e); }
}

// ── TLS ──
async function refreshCerts() {
  const certs = await api('/tls');
  const el = $('#cert-list');
  $('#cert-count').textContent = certs?.length || 0;
  if (!certs?.length) {
    el.innerHTML = '<div class="empty">No certificates. Click <b>Generate</b> to create a self-signed cert or <b>Import</b> to add an existing one.</div>';
    return;
  }
  el.innerHTML = `<table class="peer-table"><thead><tr>
    <th>Name</th><th>Subject</th><th>Issuer</th><th>Expires</th><th>Key</th><th></th>
  </tr></thead><tbody>${certs.map(c => `<tr>
    <td><b>${esc(c.name)}</b><span class="peer-addr-sub">${esc(c.id)}</span></td>
    <td>${esc(c.subject)}</td>
    <td>${esc(c.issuer)}${c.is_ca ? ' <span class="badge badge-blue">CA</span>' : ''}</td>
    <td><span style="font-family:var(--mono);font-size:12px">${esc(c.not_after)}</span></td>
    <td>${c.key_file ? '<span class="badge badge-green">yes</span>' : '<span class="badge badge-muted">no</span>'}</td>
    <td style="text-align:right"><button class="act-btn danger" onclick="deleteCert('${esc(c.id)}')">Delete</button></td>
  </tr>`).join('')}</tbody></table>`;
}

function openGenCertDialog() { $('#gen-cert-modal').style.display = ''; }
function closeGenCertDialog() { $('#gen-cert-modal').style.display = 'none'; }
function openImportCertDialog() { $('#import-cert-modal').style.display = ''; }
function closeImportCertDialog() { $('#import-cert-modal').style.display = 'none'; }

async function submitGenCert() {
  const id = $('#gen-id').value.trim(), name = $('#gen-name').value.trim(),
    domains = $('#gen-domains').value.split(',').map(s => s.trim()).filter(Boolean),
    days = parseInt($('#gen-days').value) || 365;
  if (!id || !domains.length) { toast('ID and at least one domain are required', 'error'); return; }
  try {
    await api('/tls/generate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name, domains, days }) });
    closeGenCertDialog();
    ['gen-id','gen-name','gen-domains'].forEach(x => $(`#${x}`).value = '');
    $('#gen-days').value = '365';
    refreshCerts();
  } catch (e) { toast(String(e), 'error'); }
}

async function submitImportCert() {
  const id = $('#imp-id').value.trim(), name = $('#imp-name').value.trim(),
    cert = $('#imp-cert').value.trim(), key = $('#imp-key').value.trim();
  if (!id || !cert) { toast('ID and certificate PEM are required', 'error'); return; }
  try {
    await api('/tls/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, name, cert, key }) });
    closeImportCertDialog();
    ['imp-id','imp-name','imp-cert','imp-key'].forEach(x => $(`#${x}`).value = '');
    refreshCerts();
  } catch (e) { toast(String(e), 'error'); }
}

async function deleteCert(id) {
  if (!await showConfirm('Delete Certificate', `Delete certificate "${id}"?`)) return;
  try { await api(`/tls/${id}`, { method: 'DELETE' }); refreshCerts(); toast(`Deleted ${id}`, 'success'); } catch (e) { toast(String(e), 'error'); }
}

// ── Init ──
(async function init() {
  // Auto-login if remembered credentials exist and no active session
  if (!sessionStorage.getItem('token')) {
    const saved = JSON.parse(localStorage.getItem('hy2scale_cred') || 'null');
    if (saved) {
      try {
        const r = await fetch(basePath + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: saved.u, password: saved.p }) });
        if (r.ok) {
          sessionStorage.setItem('token', (await r.json()).token);
        } else {
          localStorage.removeItem('hy2scale_cred'); // credentials stale
        }
      } catch (e) {}
    }
  }
  routeFromURL();
  if (sessionStorage.getItem('token') && location.pathname.replace(basePath, '').replace(/^\/+/, '') !== 'login') {
    refresh();
  }
})();
