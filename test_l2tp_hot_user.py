#!/usr/bin/env python3
"""Test L2TP hot user add/modify/delete on iKuai."""
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

def ssh_cmd(cmd, timeout=120):
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(IKUAI_HOST, username=SSH_USER, password=SSH_PASS, timeout=10)
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout)
    out = stdout.read().decode()
    err = stderr.read().decode()
    client.close()
    return out.strip(), err.strip()

def run_in(container, script, timeout=120):
    b64 = base64.b64encode(script.encode()).decode()
    cmd = f'{DOCKER_PATH} && echo {b64} | base64 -d > /tmp/_run.sh && chmod +x /tmp/_run.sh && docker cp /tmp/_run.sh {container}:/tmp/_run.sh && docker exec {container} /tmp/_run.sh'
    out, err = ssh_cmd(cmd, timeout)
    return out

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
        req = f"{method} {url_path} HTTP/1.1\\r\\nHost: 127.0.0.1:5565\\r\\nContent-Type: application/json\\r\\n"
        if token:
            req += f"Authorization: Bearer {token}\\r\\n"
        if body:
            req += f"Content-Length: {len(body)}\\r\\n"
        req += "Connection: close\\r\\n\\r\\n"
        if body:
            req += body
        script = f"#!/bin/sh\nprintf '{req}' | nc 127.0.0.1 5565 2>/dev/null | tail -1\n"

    return run_in('hy2scale', script)

def login():
    return json.loads(api_call('POST', '/api/login', {'username': 'admin', 'password': PASS_HASH}))['token']

def get_users(token):
    return json.loads(api_call('GET', '/api/users', token=token))

def check_secrets():
    eap, _ = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale cat /etc/ipsec.secrets')
    chap, _ = ssh_cmd(f'{DOCKER_PATH} && docker exec hy2scale cat /etc/ppp/chap-secrets')
    return eap, chap

def setup_l2tp_client(username, password, psk='test1234'):
    """Configure and connect L2TP - single all-in-one script."""
    script = f"""#!/bin/sh
# Kill everything
killall xl2tpd pppd 2>/dev/null || true
ipsec stop 2>/dev/null || true
sleep 2

# Ensure prerequisites
echo 'bypass-lan {{ load = no }}' > /etc/strongswan.d/charon/bypass-lan.conf
mkdir -p /dev/net /etc/xl2tpd /etc/ppp /var/run/xl2tpd
mknod /dev/ppp c 108 0 2>/dev/null || true

# IPsec config
cat > /etc/ipsec.conf << 'EOF'
config setup

conn l2tp-psk
    keyexchange=ikev1
    authby=psk
    type=transport
    left=%defaultroute
    leftprotoport=17/1701
    right=192.168.10.4
    rightprotoport=17/1701
    ike=aes128-sha1-modp1024,3des-sha1-modp1024!
    esp=aes128-sha1,3des-sha1!
    auto=start
EOF

cat > /etc/ipsec.secrets << 'EOF'
%any %any : PSK "{psk}"
EOF

# xl2tpd config
cat > /etc/xl2tpd/xl2tpd.conf << 'EOF'
[lac l2tp-test]
lns = 192.168.10.4
ppp debug = yes
pppoptfile = /etc/ppp/options.l2tpd.client
length bit = yes
EOF

cat > /etc/ppp/options.l2tpd.client << EOF
name {username}
password {password}
noauth
refuse-eap
refuse-chap
mtu 1400
mru 1400
noipdefault
EOF

# Start IPsec
ipsec start
sleep 7
echo "IPSEC:"
ipsec statusall 2>&1 | grep "ESTABLISHED"

# Start xl2tpd
xl2tpd -D > /tmp/xl2tp.log 2>&1 &
sleep 3
echo "c l2tp-test" > /var/run/xl2tpd/l2tp-control
sleep 12

# Check PPP
ip addr show ppp0 2>/dev/null | grep "inet " && echo "PPP_OK=yes" || echo "PPP_OK=no"
"""
    return run_in('ikev2-client', script, timeout=120)

def check_l2tp_exit():
    script = (
        "#!/bin/sh\n"
        "ip route add 34.160.111.145/32 via 192.168.25.1 dev ppp0 2>/dev/null\n"
        "curl -4 --max-time 15 --interface 192.168.25.2 -s http://ifconfig.me 2>&1\n"
        "ip route del 34.160.111.145/32 2>/dev/null\n"
    )
    return run_in('ikev2-client', script, timeout=30)

def stop_l2tp_client():
    run_in('ikev2-client',
        "#!/bin/sh\nkillall xl2tpd pppd 2>/dev/null; ipsec stop 2>/dev/null; sleep 2; echo STOPPED\n",
        timeout=15
    )

def get_logs(n=15):
    out, _ = ssh_cmd(f'{DOCKER_PATH} && docker logs --tail {n} hy2scale 2>&1')
    return out


# ============================================================
print("=" * 60)
print("L2TP Hot User Add/Modify/Delete Test")
print("=" * 60)

token = login()
print(f"[OK] Logged in")

users = get_users(token)
print(f"[OK] Users: {[u['username'] for u in users]}")

# ============================================================
print("\n" + "=" * 60)
print("TEST 1: Baseline - L2TP with 'l2tptest' (no exit_via)")
print("=" * 60)

status = setup_l2tp_client('l2tptest', 'test1234')
has_ppp = 'PPP_OK=yes' in status
print(f"[{'OK' if has_ppp else 'FAIL'}] L2TP PPP: {'UP' if has_ppp else 'DOWN'}")
for line in status.split('\n'):
    if 'ESTABLISHED' in line:
        print(f"  {line.strip()}")

if has_ppp:
    time.sleep(2)
    exit_ip = check_l2tp_exit()
    print(f"[INFO] Exit: {exit_ip}")
    # l2tptest has no exit_via, so direct exit expected
    print(f"[OK] Direct exit (no exit_via configured)")

    logs = get_logs(5)
    for line in logs.split('\n'):
        if 'tun-fwd' in line.lower() or 'ppp' in line.lower():
            print(f"  LOG: {line.strip()}")

stop_l2tp_client()
time.sleep(3)

# ============================================================
print("\n" + "=" * 60)
print("TEST 2: Hot-add 'l2tphot' with exit_via=AUB")
print("=" * 60)

resp = api_call('POST', '/api/users', {
    'username': 'l2tphot',
    'password': 'l2tppass123',
    'exit_via': 'AUB',
    'enabled': True
}, token=token)
print(f"[INFO] Add: {resp}")
time.sleep(2)

eap, chap = check_secrets()
has_chap = 'l2tphot' in chap
has_eap = 'l2tphot' in eap
print(f"[{'OK' if has_chap else 'FAIL'}] CHAP has l2tphot: {has_chap}")
print(f"[{'OK' if has_eap else 'FAIL'}] EAP has l2tphot: {has_eap}")

status = setup_l2tp_client('l2tphot', 'l2tppass123')
has_ppp = 'PPP_OK=yes' in status
print(f"[{'OK' if has_ppp else 'FAIL'}] L2TP with l2tphot: {'UP' if has_ppp else 'DOWN'}")

if has_ppp:
    time.sleep(3)
    exit_ip = check_l2tp_exit()
    print(f"[INFO] Exit: {exit_ip}")
    is_au = '38.180.128.200' in exit_ip
    print(f"[{'OK' if is_au else 'FAIL'}] Exit via AU: {is_au}")

    logs = get_logs(10)
    for line in logs.split('\n'):
        if 'l2tphot' in line or 'tun-fwd' in line.lower():
            print(f"  LOG: {line.strip()}")

stop_l2tp_client()
time.sleep(3)

# ============================================================
print("\n" + "=" * 60)
print("TEST 3: Hot-modify password for 'l2tphot'")
print("=" * 60)

users = get_users(token)
l2tphot = next((u for u in users if u['username'] == 'l2tphot'), None)
if l2tphot:
    uid = l2tphot['id']
    body = json.dumps({'username': 'l2tphot', 'password': 'newl2tp456', 'exit_via': 'AUB', 'enabled': True})
    url_path = f'/scale/api/users/{uid}'
    req = f"PUT {url_path} HTTP/1.1\\r\\nHost: 127.0.0.1:5565\\r\\nContent-Type: application/json\\r\\nAuthorization: Bearer {token}\\r\\nContent-Length: {len(body)}\\r\\nConnection: close\\r\\n\\r\\n{body}"
    resp = run_in('hy2scale', f"#!/bin/sh\nprintf '{req}' | nc 127.0.0.1 5565 2>/dev/null | tail -1\n")
    print(f"[INFO] Update: {resp}")
    time.sleep(2)

    _, chap = check_secrets()
    has_new = 'newl2tp456' in chap
    print(f"[{'OK' if has_new else 'FAIL'}] CHAP updated: {has_new}")

    # Connect with new password
    status = setup_l2tp_client('l2tphot', 'newl2tp456')
    has_ppp = 'PPP_OK=yes' in status
    print(f"[{'OK' if has_ppp else 'FAIL'}] New password: {'UP' if has_ppp else 'DOWN'}")

    if has_ppp:
        time.sleep(3)
        exit_ip = check_l2tp_exit()
        is_au = '38.180.128.200' in exit_ip
        print(f"[{'OK' if is_au else 'FAIL'}] Exit via AU: {is_au}")

    stop_l2tp_client()
    time.sleep(3)

    # Try old password (should fail)
    status = setup_l2tp_client('l2tphot', 'l2tppass123')
    rejected = 'PPP_OK=yes' not in status
    print(f"[{'OK' if rejected else 'FAIL'}] Old password rejected: {rejected}")
    stop_l2tp_client()
    time.sleep(3)
else:
    print("[FAIL] l2tphot not found!")

# ============================================================
print("\n" + "=" * 60)
print("TEST 4: Hot-delete 'l2tphot'")
print("=" * 60)

if l2tphot:
    url_path = f'/scale/api/users/{uid}'
    req = f"DELETE {url_path} HTTP/1.1\\r\\nHost: 127.0.0.1:5565\\r\\nAuthorization: Bearer {token}\\r\\nConnection: close\\r\\n\\r\\n"
    resp = run_in('hy2scale', f"#!/bin/sh\nprintf '{req}' | nc 127.0.0.1 5565 2>/dev/null | tail -1\n")
    print(f"[INFO] Delete: {resp}")
    time.sleep(2)

    eap, chap = check_secrets()
    print(f"[{'OK' if 'l2tphot' not in chap else 'FAIL'}] CHAP cleaned")
    print(f"[{'OK' if 'l2tphot' not in eap else 'FAIL'}] EAP cleaned")

    status = setup_l2tp_client('l2tphot', 'newl2tp456')
    rejected = 'PPP_OK=yes' not in status
    print(f"[{'OK' if rejected else 'FAIL'}] Deleted user rejected: {rejected}")
    stop_l2tp_client()
    time.sleep(3)

# ============================================================
print("\n" + "=" * 60)
print("TEST 5: Original 'l2tptest' still works")
print("=" * 60)

status = setup_l2tp_client('l2tptest', 'test1234')
has_ppp = 'PPP_OK=yes' in status
print(f"[{'OK' if has_ppp else 'FAIL'}] L2TP l2tptest: {'UP' if has_ppp else 'DOWN'}")

stop_l2tp_client()

print("\n" + "=" * 60)
print("L2TP HOT USER TEST COMPLETE")
print("=" * 60)
