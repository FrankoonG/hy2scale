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

function esc(s) { if (!s) return ''; const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }

// ── Navigation ──
function switchPage(name) {
  $$('.nav-item[data-page]').forEach(n => n.classList.toggle('active', n.dataset.page === name));
  $$('.page').forEach(p => p.style.display = 'none');
  $(`#page-${name}`).style.display = '';
  $('#page-title').textContent = { nodes: 'Nodes', proxies: 'Proxies', settings: 'Settings' }[name];
  if (name === 'settings') loadSettings();
}

// ── Auth ──
function showLogin() { $('#login-screen').style.display = ''; $('#app').style.display = 'none'; }
function showApp() { $('#login-screen').style.display = 'none'; $('#app').style.display = ''; }

async function doLogin() {
  const username = $('#login-user').value.trim(), password = $('#login-pass').value;
  $('#login-error').textContent = '';
  try {
    const r = await fetch(basePath + '/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
    if (!r.ok) { $('#login-error').textContent = 'Invalid username or password'; return; }
    sessionStorage.setItem('token', (await r.json()).token);
    showApp(); refresh();
  } catch (e) { $('#login-error').textContent = String(e); }
}
function doLogout() { sessionStorage.removeItem('token'); showLogin(); clearInterval(pollTimer); }
$('#login-pass').addEventListener('keydown', e => { if (e.key === 'Enter') doLogin(); });
$('#login-user').addEventListener('keydown', e => { if (e.key === 'Enter') $('#login-pass').focus(); });

// ── Polling ──
let pollTimer = null, lastTopoJSON = '';

async function refresh() {
  try {
    const node = await api('/node');
    $('#node-badge').textContent = node.node_id;
    $('#node-name-display').textContent = node.name !== node.node_id ? node.name : '';
    await Promise.all([refreshTopology(), refreshProxies()]);
  } catch (e) { console.error(e); }
  clearInterval(pollTimer);
  pollTimer = setInterval(refresh, 5000);
}

// ── Topology Table ──
function latencyHTML(ms) {
  if (ms === -1) return '<span class="latency latency-off">offline</span>';
  if (ms === 0) return '<span class="latency latency-na">—</span>';
  const cls = ms < 80 ? 'latency-good' : ms < 200 ? 'latency-med' : 'latency-bad';
  return `<span class="latency ${cls}">${ms}ms</span>`;
}

function dirHTML(dir) {
  return dir === 'inbound'
    ? '<span class="badge badge-blue">IN</span>'
    : '<span class="badge badge-orange">OUT</span>';
}

function peerRowHTML(n, isSub) {
  const cls = (n.disabled ? ' class="disabled"' : '') + (isSub ? ' class="sub-row"' : '');
  const trCls = [isSub ? 'sub-row' : '', n.disabled ? 'disabled' : ''].filter(Boolean).join(' ');

  const nameCell = isSub
    ? `<td class="col-name"><span class="peer-name-cell">${esc(n.name)}</span></td>`
    : `<td class="col-name"><span class="peer-name-cell">${esc(n.name)}</span>${n.addr ? `<span class="peer-addr-sub">${esc(n.addr)}</span>` : ''}</td>`;

  const nestedCell = (!isSub && n.direction === 'outbound')
    ? `<td class="col-nested"><label class="toggle"><input type="checkbox" ${n.nested ? 'checked' : ''} onchange="toggleNested('${esc(n.name)}',this.checked)"><span class="slider"></span></label></td>`
    : '<td class="col-nested"></td>';

  const actionsCell = isSub ? '<td class="col-actions"></td>' : `<td class="col-actions">
    <div class="act-group">
      ${n.direction === 'outbound' ? `
        <button class="act-btn ${n.disabled ? 'enable' : 'warn'}" onclick="toggleDisable('${esc(n.name)}',${!n.disabled})">${n.disabled ? 'Enable' : 'Disable'}</button>
        <button class="act-btn danger" onclick="removeClient('${esc(n.name)}')">Delete</button>
      ` : ''}
    </div>
  </td>`;

  const exitBadge = n.exit_node ? ' <span class="badge badge-green">EXIT</span>' : '';

  return `<tr class="${trCls}">
    <td class="col-latency">${isSub ? '<span class="latency latency-na">—</span>' : latencyHTML(n.latency_ms)}</td>
    <td class="col-dir">${isSub ? '' : dirHTML(n.direction)}</td>
    ${nameCell.replace('</span>', `</span>${exitBadge}`)}
    ${nestedCell}
    ${actionsCell}
  </tr>`;
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
    rows += peerRowHTML(n, false);
    if (n.children?.length) {
      for (const c of n.children) {
        count++;
        rows += peerRowHTML({ name: c.name, exit_node: c.exit_node, latency_ms: 0, direction: '', connected: true }, true);
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
  catch (e) { alert(e); }
}

async function toggleDisable(name, disabled) {
  try { await api(`/clients/${name}/disable`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ disabled }) }); lastTopoJSON = ''; refreshTopology(); }
  catch (e) { alert(e); }
}

async function removeClient(name) {
  if (!confirm(`Delete "${name}"?`)) return;
  try { await api(`/clients/${name}`, { method: 'DELETE' }); lastTopoJSON = ''; refreshTopology(); }
  catch (e) { alert(e); }
}

// ── Add Node Modal ──
function openAddDialog() { $('#add-node-modal').style.display = ''; switchModalTab($$('#add-node-modal .modal-tab')[0]); }
function closeAddDialog() { $('#add-node-modal').style.display = 'none'; }
function switchModalTab(tab) {
  const modal = tab.closest('.modal-body');
  modal.querySelectorAll('.modal-tab').forEach(t => t.classList.toggle('active', t === tab));
  modal.querySelectorAll('.modal-panel').forEach(p => p.classList.toggle('active', p.id === 'mtab-' + tab.dataset.mtab));
}

async function submitAddNode() {
  const name = $('#add-name').value.trim(), addr = $('#add-addr').value.trim(), password = $('#add-pass').value.trim();
  if (!name || !addr || !password) return alert('Name, address and password are required');
  const body = {
    name, addr, password,
    sni: $('#add-sni').value.trim(), insecure: $('#add-insecure').checked, ca: $('#add-ca').value.trim(),
    max_tx: parseInt($('#add-tx').value) || 0, max_rx: parseInt($('#add-rx').value) || 0,
    init_stream_window: parseInt($('#add-isw').value) || 0, max_stream_window: parseInt($('#add-msw').value) || 0,
    init_conn_window: parseInt($('#add-icw').value) || 0, max_conn_window: parseInt($('#add-mcw').value) || 0,
    fast_open: $('#add-fastopen').checked,
  };
  try {
    await api('/clients', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    closeAddDialog();
    ['add-name','add-addr','add-pass','add-sni','add-ca','add-tx','add-rx','add-isw','add-msw','add-icw','add-mcw'].forEach(id => $(`#${id}`).value = '');
    $('#add-insecure').checked = true; $('#add-fastopen').checked = false;
    lastTopoJSON = ''; setTimeout(refreshTopology, 1000);
  } catch (e) { alert(e); }
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
  if (!id || !listen) return alert('ID and listen address are required');
  try {
    await api('/proxies', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id, protocol, listen, exit_via }) });
    closeProxyDialog(); ['px-id','px-listen','px-exit'].forEach(id => $(`#${id}`).value = ''); refreshProxies();
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
    $('#ui-listen').value = ui.listen || ''; $('#ui-basepath').value = ui.base_path || '';
    const si = $('#server-tls-info');
    si.innerHTML = node.server
      ? `<div style="font-size:13px;text-align:left"><div style="margin-bottom:8px">Listening on <code style="background:var(--bg);padding:2px 6px;border-radius:4px">${esc(node.server.listen)}</code> (UDP)</div><div style="color:var(--text-muted)">TLS: ${node.server.tls_cert ? '<span class="badge badge-green">custom cert</span>' : '<span class="badge badge-orange">self-signed</span>'}</div></div>`
      : '<div style="font-size:13px">No hy2 server — client-only node</div>';
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

// ── Init ──
if (sessionStorage.getItem('token')) { showApp(); refresh(); } else { showLogin(); }
