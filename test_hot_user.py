#!/usr/bin/env python3
"""Test hot user add/modify/delete for IKEv2 and L2TP on iKuai."""
import paramiko
import json
import time
import base64

IKUAI_HOST = '10.130.32.40'
SSH_USER = 'sshd'
SSH_PASS = 'test1234'
DOCKER_PATH = 'export PATH=/tmp/ikpkg/docker-bin:$PATH'
API_BASE = 'http://127.0.0.1:5565/scale'
PASS_HASH = '8c6976e5b5410415bde908bd4dee15dfb167a9c873fc4bb8a81f6f2ab448a918'

def ssh_cmd(cmd, timeout=60):
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(IKUAI_HOST, username=SSH_USER, password=SSH_PASS, timeout=10)
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout)
    out = stdout.read().decode()
    err = stderr.read().decode()
    rc = stdout.channel.recv_exit_status()
    client.close()
    return out.strip(), err.strip(), rc

def run_script_in_container(container, script, timeout=60):
    """Run a shell script inside a container via base64 to avoid quoting issues."""
    b64 = base64.b64encode(script.encode()).decode()
    cmd = f'{DOCKER_PATH} && echo {b64} | base64 -d > /tmp/_run.sh && chmod +x /tmp/_run.sh && docker cp /tmp/_run.sh {container}:/tmp/_run.sh && docker exec {container} /tmp/_run.sh'
    out, err, rc = ssh_cmd(cmd, timeout=timeout)
    return out, err, rc

def api_call(method, path, data=None, token=None):
    url_path = f'/scale{path}'
    body = json.dumps(data) if data else ''

    if method in ('GET', 'POST'):
        auth_header = ''
        if token:
            auth_header = f"--header 'Authorization: Bearer {token}'"
        if method == 'GET':
            script = f"#!/bin/sh\nwget -qO- {auth_header} '{API_BASE}{path}'\n"
        else:
            script = f"#!/bin/sh\nwget -qO- --post-data='{body}' --header 'Content-Type: application/json' {auth_header} '{API_BASE}{path}'\n"
    else:
        # PUT/DELETE: BusyBox wget doesn't support --method, use printf+nc
        req = f"{method} {url_path} HTTP/1.1\\r\\nHost: 127.0.0.1:5565\\r\\nContent-Type: application/json\\r\\n"
        if token:
            req += f"Authorization: Bearer {token}\\r\\n"
        if body:
            req += f"Content-Length: {len(body)}\\r\\n"
        req += "Connection: close\\r\\n\\r\\n"
        if body:
            req += body
        script = f"#!/bin/sh\nprintf '{req}' | nc 127.0.0.1 5565 2>/dev/null | tail -1\n"

    out, err, rc = run_script_in_container('hy2scale', script)
    return out

def api_put(path, data, token):
    return api_call('PUT', path, data, token)

def login():
    resp = api_call('POST', '/api/login', {'username': 'admin', 'password': PASS_HASH})
    return json.loads(resp)['token']

def get_users(token):
    return json.loads(api_call('GET', '/api/users', token=token))

def check_secrets():
    eap, _, _ = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale cat /etc/ipsec.secrets')
    chap, _, _ = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale cat /etc/ppp/chap-secrets')
    return eap, chap

def ensure_client_ca():
    """Copy CA cert from server to client if not present."""
    out, _, _ = ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ls /etc/ipsec.d/cacerts/ca.pem 2>/dev/null || echo MISSING')
    if 'MISSING' in out:
        ssh_cmd(f'{DOCKER_PATH} && docker cp hy2scale:/etc/swanctl/x509ca/ca.pem /tmp/ca.pem && docker cp /tmp/ca.pem ikev2-client:/etc/ipsec.d/cacerts/ca.pem')
        print("[INFO] CA cert copied to client")

def setup_ikev2_client(username, password):
    """Configure and start IKEv2 client."""
    ensure_client_ca()
    # Stop existing
    ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ipsec stop 2>/dev/null; sleep 1', timeout=15)

    script = f"""#!/bin/sh
cat > /etc/ipsec.conf << 'EOCONF'
config setup

conn ikev2-test
    keyexchange=ikev2
    ike=aes256-sha256-modp2048!
    esp=aes256-sha256!
    type=tunnel
    left=%defaultroute
    leftid={username}
    leftauth=eap-mschapv2
    eap_identity={username}
    leftsourceip=%config
    right=192.168.10.4
    rightid=%any
    rightsubnet=0.0.0.0/0
    auto=start
EOCONF

cat > /etc/ipsec.secrets << EOSEC
{username} : EAP "{password}"
EOSEC

ipsec start
sleep 6
ipsec statusall 2>&1
"""
    out, err, rc = run_script_in_container('ikev2-client', script, timeout=60)
    return out

def check_ikev2_exit():
    script = """#!/bin/sh
VIP=$(ip addr show 2>/dev/null | grep "inet 192.168.26" | head -1 | awk '{print $2}' | cut -d/ -f1)
echo "VPN_IP=$VIP"
if [ -n "$VIP" ]; then
    curl --max-time 15 --interface $VIP -s http://ifconfig.me 2>&1
else
    echo NO_VPN_IP
fi
"""
    out, err, rc = run_script_in_container('ikev2-client', script, timeout=30)
    return out

def check_swanctl_sas():
    out, _, _ = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale swanctl --list-sas 2>&1')
    return out

def get_logs(n=30):
    out, _, _ = ssh_cmd(f'{DOCKER_PATH} && docker logs --tail {n} hy2scale 2>&1')
    return out


# ============================================================
print("=" * 60)
print("IKEv2 Hot User Add/Modify/Delete Test")
print("=" * 60)

token = login()
print(f"[OK] Logged in, token: {token[:8]}...")

users = get_users(token)
print(f"[OK] Current users: {[u['username'] for u in users]}")

eap, chap = check_secrets()
print(f"\n--- Baseline EAP ---\n{eap}")

# ============================================================
print("\n" + "=" * 60)
print("TEST 1: Baseline - IKEv2 with existing user 'iklocal'")
print("=" * 60)

status = setup_ikev2_client('iklocal', 'test1234')
established = 'ESTABLISHED' in status
print(f"[{'OK' if established else 'FAIL'}] IKEv2: {'ESTABLISHED' if established else 'FAILED'}")
for line in status.split('\n'):
    if 'ESTABLISHED' in line or 'INSTALLED' in line or 'installed' in line:
        print(f"  {line.strip()}")

time.sleep(3)
exit_ip = check_ikev2_exit()
print(f"[INFO] Exit: {exit_ip}")
is_au = '38.180.128.200' in exit_ip
print(f"[{'OK' if is_au else 'FAIL'}] Exit via AU: {is_au}")

sas = check_swanctl_sas()
for line in sas.split('\n'):
    if 'remote' in line.lower():
        print(f"  SAS: {line.strip()}")

ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ipsec stop 2>/dev/null', timeout=15)
time.sleep(3)

# ============================================================
print("\n" + "=" * 60)
print("TEST 2: Hot-add user 'hottest' with exit_via=AUB")
print("=" * 60)

resp = api_call('POST', '/api/users', {
    'username': 'hottest',
    'password': 'hotpass123',
    'exit_via': 'AUB',
    'enabled': True
}, token=token)
print(f"[INFO] Add user: {resp}")
time.sleep(2)

eap, chap = check_secrets()
has_eap = 'hottest' in eap
has_chap = 'hottest' in chap
print(f"[{'OK' if has_eap else 'FAIL'}] EAP has 'hottest': {has_eap}")
print(f"[{'OK' if has_chap else 'FAIL'}] CHAP has 'hottest': {has_chap}")

status = setup_ikev2_client('hottest', 'hotpass123')
established = 'ESTABLISHED' in status
print(f"[{'OK' if established else 'FAIL'}] IKEv2 with hottest: {'ESTABLISHED' if established else 'FAILED'}")
for line in status.split('\n'):
    if 'ESTABLISHED' in line or 'error' in line.lower() or 'failed' in line.lower():
        print(f"  {line.strip()}")

if established:
    time.sleep(5)
    exit_ip = check_ikev2_exit()
    print(f"[INFO] Exit: {exit_ip}")
    is_au = '38.180.128.200' in exit_ip
    print(f"[{'OK' if is_au else 'FAIL'}] Exit via AU: {is_au}")

    sas = check_swanctl_sas()
    for line in sas.split('\n'):
        if 'remote' in line.lower():
            print(f"  SAS: {line.strip()}")

    logs = get_logs(20)
    for line in logs.split('\n'):
        if 'EAP identity' in line or 'hottest' in line or 'tun-fwd' in line:
            print(f"  LOG: {line.strip()}")

ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ipsec stop 2>/dev/null', timeout=15)
time.sleep(3)

# ============================================================
print("\n" + "=" * 60)
print("TEST 3: Hot-modify password for 'hottest'")
print("=" * 60)

users = get_users(token)
hottest_user = next((u for u in users if u['username'] == 'hottest'), None)
if hottest_user:
    uid = hottest_user['id']
    resp = api_put(f'/api/users/{uid}', {
        'username': 'hottest',
        'password': 'newpass456',
        'exit_via': 'AUB',
        'enabled': True
    }, token=token)
    print(f"[INFO] Update: {resp}")
    time.sleep(2)

    eap, _ = check_secrets()
    has_new = 'newpass456' in eap
    print(f"[{'OK' if has_new else 'FAIL'}] EAP updated: {has_new}")

    status = setup_ikev2_client('hottest', 'newpass456')
    established = 'ESTABLISHED' in status
    print(f"[{'OK' if established else 'FAIL'}] New password works: {'ESTABLISHED' if established else 'FAILED'}")

    if established:
        time.sleep(3)
        exit_ip = check_ikev2_exit()
        is_au = '38.180.128.200' in exit_ip
        print(f"[{'OK' if is_au else 'FAIL'}] Exit via AU: {is_au}")

    ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ipsec stop 2>/dev/null', timeout=15)
    time.sleep(2)

    # Try old password
    status = setup_ikev2_client('hottest', 'hotpass123')
    rejected = 'ESTABLISHED' not in status
    print(f"[{'OK' if rejected else 'FAIL'}] Old password rejected: {rejected}")
    ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ipsec stop 2>/dev/null', timeout=15)
    time.sleep(3)
else:
    print("[FAIL] hottest not found!")

# ============================================================
print("\n" + "=" * 60)
print("TEST 4: Hot-delete user 'hottest'")
print("=" * 60)

if hottest_user:
    resp = api_call('DELETE', f'/api/users/{uid}', token=token)
    print(f"[INFO] Delete: {resp}")
    time.sleep(2)

    eap, chap = check_secrets()
    print(f"[{'OK' if 'hottest' not in eap else 'FAIL'}] EAP cleaned")
    print(f"[{'OK' if 'hottest' not in chap else 'FAIL'}] CHAP cleaned")

    status = setup_ikev2_client('hottest', 'newpass456')
    rejected = 'ESTABLISHED' not in status
    print(f"[{'OK' if rejected else 'FAIL'}] Deleted user rejected: {rejected}")
    ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ipsec stop 2>/dev/null', timeout=15)
    time.sleep(3)

# ============================================================
print("\n" + "=" * 60)
print("TEST 5: Original user 'iklocal' still works")
print("=" * 60)

status = setup_ikev2_client('iklocal', 'test1234')
established = 'ESTABLISHED' in status
print(f"[{'OK' if established else 'FAIL'}] IKEv2 iklocal: {'ESTABLISHED' if established else 'FAILED'}")

if established:
    time.sleep(3)
    exit_ip = check_ikev2_exit()
    is_au = '38.180.128.200' in exit_ip
    print(f"[{'OK' if is_au else 'FAIL'}] Exit via AU: {is_au}")

ssh_cmd(f'{DOCKER_PATH} && docker exec ikev2-client ipsec stop 2>/dev/null', timeout=15)

print("\n" + "=" * 60)
print("IKEv2 HOT USER TEST COMPLETE")
print("=" * 60)
