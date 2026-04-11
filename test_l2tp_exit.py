#!/usr/bin/env python3
"""Quick L2TP exit test."""
import paramiko
import base64

def ssh_cmd(cmd, timeout=120):
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect('10.130.32.40', username='sshd', password='test1234', timeout=10)
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout)
    out = stdout.read().decode()
    client.close()
    return out.strip()

def run_in(container, script, timeout=120):
    b64 = base64.b64encode(script.encode()).decode()
    cmd = f'export PATH=/tmp/ikpkg/docker-bin:$PATH && echo {b64} | base64 -d > /tmp/_run.sh && chmod +x /tmp/_run.sh && docker cp /tmp/_run.sh {container}:/tmp/_run.sh && docker exec {container} /tmp/_run.sh'
    return ssh_cmd(cmd, timeout)

script = (
    "#!/bin/sh\n"
    "killall xl2tpd pppd 2>/dev/null || true\n"
    "ipsec stop 2>/dev/null || true\n"
    "sleep 2\n"
    "echo 'bypass-lan { load = no }' > /etc/strongswan.d/charon/bypass-lan.conf\n"
    "mkdir -p /var/run/xl2tpd\n"
    "mknod /dev/ppp c 108 0 2>/dev/null || true\n"
    "\n"
    "cat > /etc/ipsec.conf << 'EOF'\n"
    "config setup\n"
    "conn l2tp-psk\n"
    "    keyexchange=ikev1\n"
    "    authby=psk\n"
    "    type=transport\n"
    "    left=%defaultroute\n"
    "    leftprotoport=17/1701\n"
    "    right=192.168.10.4\n"
    "    rightprotoport=17/1701\n"
    "    ike=aes128-sha1-modp1024,3des-sha1-modp1024!\n"
    "    esp=aes128-sha1,3des-sha1!\n"
    "    auto=start\n"
    "EOF\n"
    "\n"
    'echo \'%any %any : PSK "test1234"\' > /etc/ipsec.secrets\n'
    "\n"
    "cat > /etc/xl2tpd/xl2tpd.conf << 'EOF'\n"
    "[lac l2tp-test]\n"
    "lns = 192.168.10.4\n"
    "ppp debug = yes\n"
    "pppoptfile = /etc/ppp/options.l2tpd.client\n"
    "length bit = yes\n"
    "EOF\n"
    "\n"
    "cat > /etc/ppp/options.l2tpd.client << 'EOF'\n"
    "name l2tptest\n"
    "password test1234\n"
    "noauth\n"
    "refuse-eap\n"
    "refuse-chap\n"
    "mtu 1400\n"
    "mru 1400\n"
    "noipdefault\n"
    "EOF\n"
    "\n"
    "ipsec start\n"
    "sleep 7\n"
    "xl2tpd -D > /tmp/xl2tp.log 2>&1 &\n"
    "sleep 3\n"
    'echo "c l2tp-test" > /var/run/xl2tpd/l2tp-control\n'
    "sleep 12\n"
    "\n"
    "echo '=== PPP ==='\n"
    "ip addr show ppp0 2>&1\n"
    "echo ''\n"
    "echo '=== IPsec child SA ==='\n"
    "ipsec statusall 2>&1 | grep -E 'ESTABLISHED|INSTALLED'\n"
    "echo ''\n"
    "\n"
    "# Test exit with -4 to force IPv4\n"
    "ip route add 34.160.111.145/32 via 192.168.25.1 dev ppp0 2>/dev/null\n"
    "echo '=== Exit test ==='\n"
    "curl -4 -v --max-time 15 --interface 192.168.25.2 http://ifconfig.me 2>&1\n"
    "echo ''\n"
    "ip route del 34.160.111.145/32 2>/dev/null\n"
)

print("Running L2TP exit test...")
out = run_in('ikev2-client', script, timeout=120)
print(out)

# Server logs
print("\n=== Server logs ===")
out = ssh_cmd('export PATH=/tmp/ikpkg/docker-bin:$PATH && docker logs --tail 15 hy2scale 2>&1')
for line in out.split('\n'):
    if any(k in line for k in ['tun-fwd', 'xfrm-bridge', 'ppp', 'l2tp', 'session']):
        print(f"  {line.strip()}")
