const $ = s => document.querySelector(s);
const api = (path, opts) => fetch('/api' + path, opts).then(r => r.ok ? r.json() : r.text().then(t => { throw t }));

// Tabs
document.querySelectorAll('.tab').forEach(t => t.addEventListener('click', () => {
  document.querySelectorAll('.tab').forEach(x => x.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(x => x.classList.remove('active'));
  t.classList.add('active');
  $('#panel-' + t.dataset.tab).classList.add('active');
}));

// State
let nodeInfo = {};
let nestedCache = {};

async function refresh() {
  try {
    nodeInfo = await api('/node');
    $('#node-id').textContent = nodeInfo.node_id;
    $('#node-name').textContent = nodeInfo.name !== nodeInfo.node_id ? nodeInfo.name : '';
    await refreshPeers();
    await refreshProxies();
  } catch(e) { console.error(e); }
}

async function refreshPeers() {
  const [peers, clients] = await Promise.all([api('/peers'), api('/clients')]);
  const clientMap = {};
  clients.forEach(c => clientMap[c.name] = c);

  const ul = $('#peer-list');
  if (peers.length === 0 && clients.length === 0) {
    ul.innerHTML = '<li class="loading">No peers connected</li>';
    return;
  }

  // Merge: show all clients (connected or not) + any inbound peers
  const shown = new Set();
  let html = '';

  // Connected peers first
  for (const p of peers) {
    shown.add(p.name);
    const cl = clientMap[p.name];
    html += renderPeer(p, cl, true);
  }

  // Disconnected clients
  for (const c of clients) {
    if (!shown.has(c.name)) {
      html += renderPeer({ name: c.name, direction: 'outbound', exit_node: false, nested: false }, c, false);
    }
  }

  ul.innerHTML = html;

  // Load nested peers for expanded items
  for (const p of peers) {
    if (p.nested && p.direction === 'outbound') {
      loadNested(p.name);
    }
  }
}

function renderPeer(p, cl, connected) {
  const dirBadge = p.direction === 'inbound'
    ? '<span class="badge badge-in">IN</span>'
    : '<span class="badge badge-out">OUT</span>';
  const exitBadge = p.exit_node ? '<span class="badge badge-exit">EXIT</span>' : '';
  const statusBadge = connected
    ? '<span class="badge badge-connected">connected</span>'
    : '<span class="badge badge-disconnected">disconnected</span>';

  const addr = cl ? `<span style="color:var(--dim);font-size:12px;font-family:monospace">${cl.addr}</span>` : '';

  const nestedToggle = p.direction === 'outbound' ? `
    <label class="toggle" title="Nested discovery">
      <input type="checkbox" ${p.nested ? 'checked' : ''} onchange="toggleNested('${p.name}', this.checked)">
      <span class="slider"></span>
    </label>
    <span style="font-size:11px;color:var(--dim)">nested</span>
  ` : '';

  const deleteBtn = cl ? `<button class="danger" onclick="removeClient('${p.name}')" title="Disconnect">&times;</button>` : '';

  return `
    <li class="peer-item" id="peer-${p.name}">
      <span class="peer-name">${p.name}</span>
      ${dirBadge}${exitBadge}${statusBadge}
      ${addr}
      <span class="spacer"></span>
      ${nestedToggle}
      ${deleteBtn}
    </li>
    <div class="nested-peers" id="nested-${p.name}" style="display:${p.nested && connected ? 'block' : 'none'}"></div>
  `;
}

async function loadNested(name) {
  const el = $(`#nested-${name}`);
  if (!el) return;
  try {
    const peers = await api(`/peers/${name}/peers`);
    nestedCache[name] = peers;
    if (peers.length === 0) {
      el.innerHTML = '<div style="color:var(--dim);font-size:12px;padding:4px">No sub-peers</div>';
    } else {
      el.innerHTML = peers.map(p => `
        <div class="peer-item">
          <span class="peer-name">${p.name}</span>
          ${p.exit_node ? '<span class="badge badge-exit">EXIT</span>' : ''}
          <span class="badge badge-${p.direction === 'inbound' ? 'in' : 'out'}">${p.direction === 'inbound' ? 'IN' : 'OUT'}</span>
        </div>
      `).join('');
    }
    el.style.display = 'block';
  } catch(e) {
    el.innerHTML = `<div style="color:var(--red);font-size:12px;padding:4px">${e}</div>`;
    el.style.display = 'block';
  }
}

async function toggleNested(name, enabled) {
  try {
    await api(`/peers/${name}/nested`, {
      method: 'PUT',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ enabled })
    });
    const el = $(`#nested-${name}`);
    if (enabled && el) {
      loadNested(name);
    } else if (el) {
      el.style.display = 'none';
    }
  } catch(e) { alert(e); }
}

async function addClient() {
  const name = $('#cl-name').value.trim();
  const addr = $('#cl-addr').value.trim();
  const password = $('#cl-pass').value.trim();
  const bw = parseInt($('#cl-bw').value) || 0;
  if (!name || !addr || !password) { alert('Name, address and password required'); return; }
  try {
    await api('/clients', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ name, addr, password, bandwidth: bw })
    });
    $('#cl-name').value = '';
    $('#cl-addr').value = '';
    $('#cl-pass').value = '';
    $('#cl-bw').value = '';
    setTimeout(refreshPeers, 1000);
  } catch(e) { alert(e); }
}

async function removeClient(name) {
  if (!confirm(`Disconnect ${name}?`)) return;
  try {
    await api(`/clients/${name}`, { method: 'DELETE' });
    setTimeout(refreshPeers, 500);
  } catch(e) { alert(e); }
}

// --- Proxies ---

async function refreshProxies() {
  const proxies = await api('/proxies');
  const el = $('#proxy-list');
  if (!proxies || proxies.length === 0) {
    el.innerHTML = '<div class="loading">No proxies configured</div>';
    return;
  }
  el.innerHTML = proxies.map(p => `
    <div class="proxy-item">
      <span class="proxy-protocol">${p.protocol}</span>
      <span class="proxy-listen">${p.listen}</span>
      <span style="color:var(--dim)">&rarr;</span>
      <span class="proxy-exit">${p.exit_via || '(local exit)'}</span>
      <span class="spacer"></span>
      <span style="color:var(--dim);font-size:11px">${p.id}</span>
      <button class="danger" onclick="removeProxy('${p.id}')" title="Remove">&times;</button>
    </div>
  `).join('');
}

async function addProxy() {
  const id = $('#px-id').value.trim();
  const protocol = $('#px-proto').value;
  const listen = $('#px-listen').value.trim();
  const exit_via = $('#px-exit').value.trim();
  if (!id || !listen) { alert('ID and listen address required'); return; }
  try {
    await api('/proxies', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ id, protocol, listen, exit_via })
    });
    $('#px-id').value = '';
    $('#px-listen').value = '';
    $('#px-exit').value = '';
    refreshProxies();
  } catch(e) { alert(e); }
}

async function removeProxy(id) {
  if (!confirm(`Remove proxy ${id}?`)) return;
  try {
    await api(`/proxies/${id}`, { method: 'DELETE' });
    refreshProxies();
  } catch(e) { alert(e); }
}

// Init + polling
refresh();
setInterval(refresh, 5000);
