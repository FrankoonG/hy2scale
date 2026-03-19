const $ = s => document.querySelector(s);
const $$ = s => document.querySelectorAll(s);
const basePath = location.pathname.replace(/\/$/, '');

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

// ── Navigation ──
function switchPage(name) {
  $$('.nav-item[data-page]').forEach(n => n.classList.toggle('active', n.dataset.page === name));
  $$('.page').forEach(p => p.style.display = 'none');
  const page = $(`#page-${name}`);
  if (page) page.style.display = '';
  $('#page-title').textContent = { nodes: 'Nodes', proxies: 'Proxies', settings: 'Settings' }[name] || name;
  if (name === 'settings') loadSettings();
}

// ── Auth ──
function showLogin() { $('#login-screen').style.display = ''; $('#app').style.display = 'none'; }
function showApp() { $('#login-screen').style.display = 'none'; $('#app').style.display = ''; }

async function doLogin() {
  const username = $('#login-user').value.trim();
  const password = $('#login-pass').value;
  $('#login-error').textContent = '';
  try {
    const r = await fetch(basePath + '/api/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    });
    if (!r.ok) { $('#login-error').textContent = 'Invalid username or password'; return; }
    const data = await r.json();
    sessionStorage.setItem('token', data.token);
    showApp(); refresh();
  } catch (e) { $('#login-error').textContent = String(e); }
}

function doLogout() { sessionStorage.removeItem('token'); showLogin(); clearInterval(pollTimer); }

$('#login-pass').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
$('#login-user').addEventListener('keydown', e => { if (e.key === 'Enter') $('#login-pass').focus(); });

// ── Data ──
let pollTimer = null;

async function refresh() {
  try {
    const node = await api('/node');
    $('#node-badge').textContent = node.node_id;
    $('#node-name-display').textContent = node.name !== node.node_id ? node.name : '';
    await Promise.all([refreshPeers(), refreshProxies()]);
  } catch (e) { console.error(e); }
  clearInterval(pollTimer);
  pollTimer = setInterval(refresh, 5000);
}

// ── Peers ──
async function refreshPeers() {
  const [peers, clients] = await Promise.all([api('/peers'), api('/clients')]);
  const clientMap = {};
  clients.forEach(c => clientMap[c.name] = c);

  const el = $('#peer-list');
  const total = new Set([...peers.map(p => p.name), ...clients.map(c => c.name)]).size;
  $('#peer-count').textContent = total;

  if (total === 0) { el.innerHTML = '<div class="empty">No peers connected. Add a connection below.</div>'; return; }

  const shown = new Set();
  let html = '';
  for (const p of peers) { shown.add(p.name); html += peerHTML(p, clientMap[p.name], true); }
  for (const c of clients) { if (!shown.has(c.name)) html += peerHTML({ name: c.name, direction: 'outbound', exit_node: false, nested: false }, c, false); }
  el.innerHTML = html;

  for (const p of peers) { if (p.nested && p.direction === 'outbound') loadNested(p.name); }
}

function peerHTML(p, cl, connected) {
  const dir = p.direction === 'inbound'
    ? '<span class="badge badge-blue">IN</span>'
    : '<span class="badge badge-orange">OUT</span>';
  const exit = p.exit_node ? '<span class="badge badge-green">EXIT</span>' : '';
  const status = connected
    ? '<span class="badge badge-green"><span class="dot dot-green"></span> connected</span>'
    : '<span class="badge badge-red"><span class="dot dot-red"></span> disconnected</span>';
  const addr = cl ? `<span class="peer-addr">${cl.addr}</span>` : '';
  const nested = p.direction === 'outbound' ? `
    <label class="toggle"><input type="checkbox" ${p.nested ? 'checked' : ''} onchange="toggleNested('${p.name}',this.checked)"><span class="slider"></span></label>
    <span class="toggle-label">Nested</span>` : '';
  const del = cl ? `<button class="btn-icon" onclick="removeClient('${p.name}')" title="Disconnect">✕</button>` : '';
  return `
    <div class="peer-row">
      <span class="peer-name">${p.name}</span>${dir}${exit}${status}${addr}
      <span class="spacer"></span>${nested}${del}
    </div>
    <div class="nested-container" id="nested-${p.name}" style="display:${p.nested && connected ? 'block' : 'none'}"></div>`;
}

async function loadNested(name) {
  const el = $(`#nested-${name}`);
  if (!el) return;
  try {
    const peers = await api(`/peers/${name}/peers`);
    if (!peers.length) { el.innerHTML = '<div class="empty" style="padding:8px;text-align:left">No sub-peers discovered</div>'; }
    else {
      el.innerHTML = peers.map(p => `
        <div class="peer-row">
          <span class="peer-name">${p.name}</span>
          ${p.exit_node ? '<span class="badge badge-green">EXIT</span>' : ''}
          <span class="badge ${p.direction === 'inbound' ? 'badge-blue' : 'badge-orange'}">${p.direction === 'inbound' ? 'IN' : 'OUT'}</span>
        </div>`).join('');
    }
    el.style.display = 'block';
  } catch (e) { el.innerHTML = `<div class="msg-error" style="padding:8px">${e}</div>`; el.style.display = 'block'; }
}

async function toggleNested(name, enabled) {
  try {
    await api(`/peers/${name}/nested`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }) });
    const el = $(`#nested-${name}`);
    if (enabled && el) loadNested(name); else if (el) el.style.display = 'none';
  } catch (e) { alert(e); }
}

async function addClient() {
  const name = $('#cl-name').value.trim(), addr = $('#cl-addr').value.trim(),
    password = $('#cl-pass').value.trim(), bw = parseInt($('#cl-bw').value) || 0;
  if (!name || !addr || !password) return alert('Name, address and password are required');
  try {
    await api('/clients', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, addr, password, bandwidth: bw }) });
    $('#cl-name').value = ''; $('#cl-addr').value = ''; $('#cl-pass').value = ''; $('#cl-bw').value = '';
    setTimeout(refreshPeers, 1000);
  } catch (e) { alert(e); }
}

async function removeClient(name) {
  if (!confirm(`Disconnect from "${name}"?`)) return;
  try { await api(`/clients/${name}`, { method: 'DELETE' }); setTimeout(refreshPeers, 500); } catch (e) { alert(e); }
}

// ── Proxies ──
async function refreshProxies() {
  const proxies = await api('/proxies');
  const el = $('#proxy-list');
  $('#proxy-count').textContent = proxies?.length || 0;
  if (!proxies?.length) { el.innerHTML = '<div class="empty">No proxy instances. Add one below.</div>'; return; }
  el.innerHTML = proxies.map(p => `
    <div class="proxy-row">
      <span class="proxy-tag">${p.protocol}</span>
      <span class="proxy-listen">${p.listen}</span>
      <span class="proxy-arrow">→</span>
      <span class="proxy-exit">${p.exit_via || '(local exit)'}</span>
      <span class="spacer"></span>
      <span class="badge badge-muted">${p.id}</span>
      <button class="btn-icon" onclick="removeProxy('${p.id}')" title="Remove">✕</button>
    </div>`).join('');
}

async function addProxy() {
  const id = $('#px-id').value.trim(), protocol = $('#px-proto').value,
    listen = $('#px-listen').value.trim(), exit_via = $('#px-exit').value.trim();
  if (!id || !listen) return alert('ID and listen address are required');
  try {
    await api('/proxies', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, protocol, listen, exit_via }) });
    $('#px-id').value = ''; $('#px-listen').value = ''; $('#px-exit').value = '';
    refreshProxies();
  } catch (e) { alert(e); }
}

async function removeProxy(id) {
  if (!confirm(`Remove proxy "${id}"?`)) return;
  try { await api(`/proxies/${id}`, { method: 'DELETE' }); refreshProxies(); } catch (e) { alert(e); }
}

// ── Settings ──
async function loadSettings() {
  try {
    const [ui, node] = await Promise.all([api('/settings/ui'), api('/node')]);
    $('#ui-listen').value = ui.listen || '';
    $('#ui-basepath').value = ui.base_path || '';
    const si = $('#server-info');
    si.innerHTML = node.server
      ? `<div style="font-size:13px">Listening on <code style="background:var(--bg);padding:2px 6px;border-radius:4px">${node.server.listen}</code> (UDP)</div>`
      : '<div style="font-size:13px;color:var(--text-muted)">No hy2 server configured — this is a client-only node</div>';
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
    ['set-cur-pass', 'set-new-user', 'set-new-pass', 'set-confirm-pass'].forEach(id => $(`#${id}`).value = '');
    $('#set-ok').textContent = 'Credentials updated. Redirecting to login...';
    setTimeout(doLogout, 1500);
  } catch (e) { $('#set-error').textContent = String(e); }
}

async function updateUISettings() {
  const listen = $('#ui-listen').value.trim(), bp = $('#ui-basepath').value.trim();
  $('#ui-error').textContent = ''; $('#ui-ok').textContent = '';
  try {
    await api('/settings/ui', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ listen: listen || null, base_path: bp || null }) });
    $('#ui-ok').textContent = 'Saved. Restart the container to apply changes.';
  } catch (e) { $('#ui-error').textContent = String(e); }
}

// ── Init ──
if (sessionStorage.getItem('token')) { showApp(); refresh(); } else { showLogin(); }
