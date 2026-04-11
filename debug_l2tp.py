#!/usr/bin/env python3
"""Debug L2TP client connection - single-shot script."""
import paramiko
import base64

def ssh_cmd(cmd, timeout=120):
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect('10.130.32.40', username='sshd', password='test1234', timeout=10)
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout)
    out = stdout.read().decode()
    err = stderr.read().decode()
    client.close()
    return out.strip(), err.strip()

def run_in(container, script, timeout=120):
    b64 = base64.b64encode(script.encode()).decode()
    cmd = f'export PATH=/tmp/ikpkg/docker-bin:$PATH && echo {b64} | base64 -d > /tmp/_run.sh && chmod +x /tmp/_run.sh && docker cp /tmp/_run.sh {container}:/tmp/_run.sh && docker exec {container} /tmp/_run.sh'
    out, err = ssh_cmd(cmd, timeout)
    return out

# All-in-one L2TP test script
script = r"""#!/bin/sh
set -e

echo "=== Step 1: Setup ==="
echo 'bypass-lan { load = no }' > /etc/strongswan.d/charon/bypass-lan.conf
mkdir -p /dev/net /etc/xl2tpd /etc/ppp /var/run/xl2tpd
mknod /dev/ppp c 108 0 2>/dev/null || true

# Kill everything
killall xl2tpd pppd 2>/dev/null || true
ipsec stop 2>/dev/null || true
sleep 2

echo "=== Step 2: Write configs ==="
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
%any %any : PSK "test1234"
EOF

cat > /etc/xl2tpd/xl2tpd.conf << 'EOF'
[lac l2tp-test]
lns = 192.168.10.4
ppp debug = yes
pppoptfile = /etc/ppp/options.l2tpd.client
length bit = yes
EOF

cat > /etc/ppp/options.l2tpd.client << 'EOF'
name l2tptest
password test1234
noauth
refuse-eap
refuse-chap
mtu 1400
mru 1400
noipdefault
EOF

echo "=== Step 3: Start IPsec ==="
ipsec start
sleep 7
ipsec statusall 2>&1 | grep -E "ESTABLISHED|INSTALLED|Security"

echo "=== Step 4: Start xl2tpd ==="
xl2tpd -D > /tmp/xl2tp.log 2>&1 &
sleep 3
echo "c l2tp-test" > /var/run/xl2tpd/l2tp-control
echo "Waiting for PPP..."
sleep 12

echo "=== Step 5: Check PPP ==="
ip addr show ppp0 2>&1
echo "=== PPP procs ==="
ps | grep -E "ppp|xl2tp" | grep -v grep
echo "=== xl2tpd log ==="
cat /tmp/xl2tp.log 2>/dev/null | tail -25
echo "=== DONE ==="
"""

print("Running L2TP test...")
out = run_in('ikev2-client', script, timeout=120)
print(out)

# Server logs
print("\n=== Server logs ===")
out, _ = ssh_cmd('export PATH=/tmp/ikpkg/docker-bin:$PATH && docker logs --tail 20 hy2scale 2>&1')
for line in out.split('\n'):
    if any(k in line.lower() for k in ['l2tp', 'ppp', 'xl2tp', 'session']):
        print(f"  {line.strip()}")
