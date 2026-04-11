#!/usr/bin/env python3
"""Debug raw socket writing to ipsec0."""
import paramiko
import base64

def ssh(cmd, timeout=30):
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect('10.130.32.40', username='sshd', password='test1234', timeout=10)
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout)
    return stdout.read().decode().strip()

def run_in(script, timeout=30):
    b64 = base64.b64encode(script.encode()).decode()
    cmd = f'export PATH=/tmp/ikpkg/docker-bin:$PATH && echo {b64} | base64 -d > /tmp/_r.sh && chmod +x /tmp/_r.sh && docker cp /tmp/_r.sh hy2scale:/tmp/_r.sh && docker exec hy2scale /tmp/_r.sh'
    return ssh(cmd, timeout)

# 1. Check routing
out = run_in("#!/bin/sh\nip route get 192.168.26.2 2>&1\n")
print(f"Route to VPN client: {out}")

# 2. Check ipsec0 TX with different packet sizes
script = (
    "#!/bin/sh\n"
    "BEFORE=$(cat /proc/net/dev | grep ipsec0 | awk '{print $10}')\n"
    "echo TX_BEFORE=$BEFORE\n"
    "# Small packet\n"
    "python3 -c \"\n"
    "import socket, struct\n"
    "s = socket.socket(socket.AF_INET, socket.SOCK_RAW, socket.IPPROTO_RAW)\n"
    "s.setsockopt(socket.IPPROTO_IP, socket.IP_HDRINCL, 1)\n"
    "s.setsockopt(socket.SOL_SOCKET, 25, b'ipsec0')\n"
    "for size in [100, 500, 1000, 1200, 1300]:\n"
    "    hdr = struct.pack('!BBHHHBBH4s4s', 0x45, 0, size, 0, 0x4000, 64, 6, 0,\n"
    "        socket.inet_aton('169.254.99.1'), socket.inet_aton('192.168.26.2'))\n"
    "    pkt = hdr + b'\\\\x00' * (size-20)\n"
    "    try:\n"
    "        s.sendto(pkt, ('192.168.26.2', 0))\n"
    "        print(f'{size}b: OK')\n"
    "    except Exception as e:\n"
    "        print(f'{size}b: ERROR {e}')\n"
    "s.close()\n"
    "\" 2>&1\n"
    "AFTER=$(cat /proc/net/dev | grep ipsec0 | awk '{print $10}')\n"
    "echo TX_AFTER=$AFTER\n"
)
out = run_in(script, timeout=15)
print(f"\nRaw socket test:\n{out}")
