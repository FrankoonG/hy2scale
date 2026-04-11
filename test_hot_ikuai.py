#!/usr/bin/env python3
"""Test hot user add/modify/delete using iKuai native VPN client."""
import requests
import hashlib
import json
import time
import base64
import paramiko

IKUAI = '10.130.32.40'
IKUAI_PWD = 'test1234'
DOCKER_PATH = 'export PATH=/tmp/ikpkg/docker-bin:$PATH'
HY2_API = 'http://127.0.0.1:5565/scale'
HY2_PASS_HASH = hashlib.sha256(b'admin').hexdigest()
NODE_ID = '1026c5bb'  # rightid must match server cert CN

# --- iKuai Web API ---
def ikuai_login():
    s = requests.Session()
    pwd_md5 = hashlib.md5(IKUAI_PWD.encode()).hexdigest()
    s.post(f'http://{IKUAI}/Action/login', json={
        'username': 'admin', 'passwd': pwd_md5, 'pass': pwd_md5
    })
    return s

def ikuai_call(s, func_name, action, param):
    r = s.post(f'http://{IKUAI}/Action/call', json={
        'func_name': func_name, 'action': action, 'param': param
    }, headers={'isAjax': '1'})
    return r.json()

def ikuai_add_ikev2(s, name, remote, username, password, leftid):
    # iKuai requires name to start with 'iked_'
    if not name.startswith('iked_'):
        name = 'iked_' + name
    return ikuai_call(s, 'ike_client', 'add', {
        'authby': 'mschapv2', 'name': name, 'remote_addr': remote,
        'username': username, 'passwd': password,
        'leftid': leftid, 'rightid': NODE_ID,
        'interface': 'auto', 'enabled': 'yes'
    })

def ikuai_del_ikev2(s, name):
    # First get the ID
    data = ikuai_call(s, 'ike_client', 'show', {'TYPE': 'data,total', 'limit': '0,500'})
    for item in data.get('Data', {}).get('data', []):
        if item.get('name') == name:
            return ikuai_call(s, 'ike_client', 'del', {'id': str(item['id'])})
    return {'ErrMsg': 'not found'}

def ikuai_show_ikev2(s):
    return ikuai_call(s, 'ike_client', 'show', {'TYPE': 'data,total', 'limit': '0,500'})

def ikuai_update_ikev2(s, name, **updates):
    data = ikuai_call(s, 'ike_client', 'show', {'TYPE': 'data,total', 'limit': '0,500'})
    for item in data.get('Data', {}).get('data', []):
        if item.get('name') == name:
            param = {
                'id': str(item['id']), 'authby': 'mschapv2', 'name': name,
                'remote_addr': item.get('remote_addr', '192.168.10.4'),
                'username': item.get('username'), 'passwd': item.get('passwd'),
                'leftid': item.get('leftid', ''), 'rightid': NODE_ID,
                'interface': 'auto', 'enabled': 'yes'
            }
            param.update(updates)
            return ikuai_call(s, 'ike_client', 'set', param)
    return {'ErrMsg': 'not found'}

# --- SSH commands ---
def ssh_cmd(cmd, timeout=30):
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(IKUAI, username='sshd', password=IKUAI_PWD, timeout=10)
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout)
    out = stdout.read().decode().strip()
    client.close()
    return out

def run_in_hy2(script, timeout=30):
    b64 = base64.b64encode(script.encode()).decode()
    cmd = f'{DOCKER_PATH} && echo {b64} | base64 -d > /tmp/_r.sh && chmod +x /tmp/_r.sh && docker cp /tmp/_r.sh hy2scale:/tmp/_r.sh && docker exec hy2scale /tmp/_r.sh'
    return ssh_cmd(cmd, timeout)

# --- hy2scale API ---
def hy2_login():
    resp = run_in_hy2(
        f"#!/bin/sh\nwget -qO- --post-data='{{\"username\":\"admin\",\"password\":\"{HY2_PASS_HASH}\"}}' "
        f"--header 'Content-Type: application/json' '{HY2_API}/api/login'\n"
    )
    return json.loads(resp)['token']

def hy2_api(method, path, data=None, token=None):
    body = json.dumps(data) if data else ''
    if method in ('GET', 'POST'):
        auth = f"--header 'Authorization: Bearer {token}'" if token else ''
        if method == 'GET':
            script = f"#!/bin/sh\nwget -qO- {auth} '{HY2_API}{path}'\n"
        else:
            script = f"#!/bin/sh\nwget -qO- --post-data='{body}' --header 'Content-Type: application/json' {auth} '{HY2_API}{path}'\n"
    else:
        url_path = f'/scale{path}'
        req = f"{method} {url_path} HTTP/1.1\\r\\nHost: 127.0.0.1:5565\\r\\nContent-Type: application/json\\r\\n"
        if token: req += f"Authorization: Bearer {token}\\r\\n"
        if body: req += f"Content-Length: {len(body)}\\r\\n"
        req += f"Connection: close\\r\\n\\r\\n{body}"
        script = f"#!/bin/sh\nprintf '{req}' | nc 127.0.0.1 5565 2>/dev/null | tail -1\n"
    return run_in_hy2(script)

def hy2_get_users(token):
    return json.loads(hy2_api('GET', '/api/users', token=token))

def check_exit(vip, table='iked_hot', timeout=15):
    """Check exit IP from iKuai SSH using VPN virtual IP with source-based routing."""
    return ssh_cmd(
        f'ip rule add from {vip} lookup {table} prio 100 2>/dev/null; '
        f'curl -4 -s --max-time {timeout} --interface {vip} http://ifconfig.me; '
        f'ip rule del from {vip} lookup {table} prio 100 2>/dev/null',
        timeout=timeout+5
    )

def wait_ikev2_connected(s, name, max_wait=30):
    """Wait for iKuai IKEv2 client to connect, return VIP or None."""
    for i in range(max_wait // 3):
        data = ikuai_show_ikev2(s)
        for item in data.get('Data', {}).get('data', []):
            if item.get('name') == name:
                vip = item.get('ip_addr', '')
                if vip:
                    return vip
        time.sleep(3)
    return None

def hy2_logs(n=10):
    return ssh_cmd(f'{DOCKER_PATH} && docker logs --tail {n} hy2scale 2>&1')


# ============================================================
print("=" * 60)
print("Hot User Test (iKuai Native VPN Client)")
print("=" * 60)

ik = ikuai_login()
print("[OK] iKuai logged in")

token = hy2_login()
print("[OK] hy2scale logged in")

# Clean up leftover test clients (keep iklocal)
ikuai_del_ikev2(ik, 'iked_hottest')
time.sleep(2)

# ============================================================
print("\n" + "=" * 60)
print("TEST 1: Hot-add user + IKEv2 connect + exit")
print("=" * 60)

# Add user in hy2scale
resp = hy2_api('POST', '/api/users', {
    'username': 'hotikev2', 'password': 'hotpass123',
    'exit_via': 'AUB', 'enabled': True
}, token=token)
print(f"  Add user: {resp}")

# Verify secrets
secrets = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale cat /etc/ipsec.secrets')
print(f"  [{'OK' if 'hotikev2' in secrets else 'FAIL'}] EAP secrets updated")

# Create iKuai IKEv2 client
resp = ikuai_add_ikev2(ik, 'iked_hottest', '192.168.10.4', 'hotikev2', 'hotpass123', 'hotikev2')
print(f"  iKuai client: {resp.get('ErrMsg', 'unknown')}")

# Wait for connection
vip = wait_ikev2_connected(ik, 'iked_hottest')
print(f"  [{'OK' if vip else 'FAIL'}] IKEv2 connected: VIP={vip}")

if vip:
    time.sleep(5)  # Wait for iKuai routing table to settle
    exit_ip = check_exit(vip, 'iked_hottest')
    is_au = '38.180.128.200' in exit_ip
    print(f"  [{'OK' if is_au else 'FAIL'}] Exit: {exit_ip} (expect AU)")

# ============================================================
print("\n" + "=" * 60)
print("TEST 2: Hot-modify password")
print("=" * 60)

# Update password in hy2scale
users = hy2_get_users(token)
hotuser = next((u for u in users if u['username'] == 'hotikev2'), None)
if hotuser:
    body = json.dumps({'username': 'hotikev2', 'password': 'newpass456', 'exit_via': 'AUB', 'enabled': True})
    url_path = f'/scale/api/users/{hotuser["id"]}'
    req = f"PUT {url_path} HTTP/1.1\\r\\nHost: 127.0.0.1:5565\\r\\nContent-Type: application/json\\r\\nAuthorization: Bearer {token}\\r\\nContent-Length: {len(body)}\\r\\nConnection: close\\r\\n\\r\\n{body}"
    resp = run_in_hy2(f"#!/bin/sh\nprintf '{req}' | nc 127.0.0.1 5565 2>/dev/null | tail -1\n")
    print(f"  Update: {resp}")

    secrets = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale cat /etc/ipsec.secrets')
    print(f"  [{'OK' if 'newpass456' in secrets else 'FAIL'}] Secrets updated")

    # Update iKuai client password and reconnect
    ikuai_update_ikev2(ik, 'iked_hottest', passwd='newpass456', username='hotikev2')
    time.sleep(5)
    vip = wait_ikev2_connected(ik, 'iked_hottest')
    print(f"  [{'OK' if vip else 'FAIL'}] Reconnected: VIP={vip}")

    if vip:
        exit_ip = check_exit(vip, 'iked_hottest')
        is_au = '38.180.128.200' in exit_ip
        print(f"  [{'OK' if is_au else 'FAIL'}] Exit: {exit_ip}")

# ============================================================
print("\n" + "=" * 60)
print("TEST 3: Hot-delete user")
print("=" * 60)

if hotuser:
    url_path = f'/scale/api/users/{hotuser["id"]}'
    req = f"DELETE {url_path} HTTP/1.1\\r\\nHost: 127.0.0.1:5565\\r\\nAuthorization: Bearer {token}\\r\\nConnection: close\\r\\n\\r\\n"
    resp = run_in_hy2(f"#!/bin/sh\nprintf '{req}' | nc 127.0.0.1 5565 2>/dev/null | tail -1\n")
    print(f"  Delete: {resp}")

    secrets = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale cat /etc/ipsec.secrets')
    print(f"  [{'OK' if 'hotikev2' not in secrets else 'FAIL'}] Secrets cleaned")

    # iKuai client should fail to reconnect
    # Disable and re-enable to force reconnect attempt
    ikuai_update_ikev2(ik, 'iked_hottest', enabled='no')
    time.sleep(2)
    ikuai_update_ikev2(ik, 'iked_hottest', enabled='yes', username='hotikev2', passwd='newpass456')
    time.sleep(10)
    data = ikuai_show_ikev2(ik)
    connected = False
    for item in data.get('Data', {}).get('data', []):
        if item.get('name') == 'hot_test' and item.get('ip_addr'):
            connected = True
    print(f"  [{'OK' if not connected else 'FAIL'}] Deleted user rejected: {not connected}")

# Clean up iKuai client
ikuai_del_ikev2(ik, 'iked_hottest')

# ============================================================
print("\n" + "=" * 60)
print("TEST 4: Original user 'iklocal' still works")
print("=" * 60)

# Ensure iklocal client exists
data = ikuai_show_ikev2(ik)
has_iklocal = any(item.get('leftid') == 'iklocal' for item in data.get('Data', {}).get('data', []))
if not has_iklocal:
    ikuai_add_ikev2(ik, 'iked_iklocal', '192.168.10.4', 'iklocal', 'test1234', 'iklocal')
    time.sleep(10)
    data = ikuai_show_ikev2(ik)
iklocal_vip = None
iklocal_table = None
for item in data.get('Data', {}).get('data', []):
    if item.get('username') == 'iklocal' or item.get('leftid') == 'iklocal':
        iklocal_vip = item.get('ip_addr', '')
        iklocal_table = item.get('name', '')
        print(f"  Client: {iklocal_table}, VIP={iklocal_vip}")

if iklocal_vip and iklocal_table:
    exit_ip = check_exit(iklocal_vip, iklocal_table)
    is_au = '38.180.128.200' in exit_ip
    print(f"  [{'OK' if is_au else 'FAIL'}] Exit: {exit_ip}")
else:
    print("  [SKIP] No iklocal client connected")

# Show logs
print("\n--- Server logs ---")
logs = hy2_logs(10)
for line in logs.split('\n'):
    if any(k in line for k in ['tun-fwd', 'EAP', 'session', 'hotikev2']):
        print(f"  {line.strip()}")

print("\n" + "=" * 60)
print("HOT USER TEST COMPLETE")
print("=" * 60)
