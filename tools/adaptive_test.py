#!/usr/bin/env python3
"""
Adaptive Link Quality Test - 60 second scenario
Run on VM (10.130.32.32) where Docker containers are running.
"""
import subprocess, time

def tc(delay_ms=0, loss_pct=0, drop_all=False):
    subprocess.run("docker exec hub-test tc qdisc del dev eth0 root 2>/dev/null", shell=True, capture_output=True)
    subprocess.run("docker exec hub-test iptables -D OUTPUT -d 10.99.0.10 -j DROP 2>/dev/null", shell=True, capture_output=True)
    if drop_all:
        subprocess.run("docker exec hub-test iptables -A OUTPUT -d 10.99.0.10 -j DROP", shell=True, capture_output=True)
        return
    if delay_ms > 0 or loss_pct > 0:
        loss = "loss %d%%" % loss_pct if loss_pct > 0 else ""
        subprocess.run("docker exec hub-test tc qdisc add dev eth0 root handle 1: prio", shell=True, capture_output=True)
        subprocess.run("docker exec hub-test tc qdisc add dev eth0 parent 1:1 handle 10: netem delay %dms %s" % (delay_ms, loss), shell=True, capture_output=True)
        subprocess.run("docker exec hub-test tc filter add dev eth0 parent 1:0 protocol ip u32 match ip dst 10.99.0.10/32 flowid 1:1", shell=True, capture_output=True)

def curl_test():
    start = time.time()
    try:
        r = subprocess.run(
            "curl -s --socks5-hostname testuser:testpass@10.99.0.30:11080 --connect-timeout 8 --max-time 12 http://ifconfig.me/ip",
            shell=True, capture_output=True, text=True, timeout=15)
        dur = time.time() - start
        ip = r.stdout.strip()
        return ("." in ip), dur, ip[:20] if ip else "empty"
    except:
        return False, time.time() - start, "timeout"

def get_path():
    r = subprocess.run("docker logs hub-test 2>&1 | grep adaptive | tail -1", shell=True, capture_output=True, text=True)
    l = r.stdout.strip()
    if "B/C" in l:
        return "B/C"
    if "path C " in l:
        return "DIRECT"
    return "?"

print("=" * 60)
print("ADAPTIVE LINK TEST - 60s SCENARIO")
print("=" * 60)
print()

phases = {
    0:  ("NORMAL 200ms", 200, 0, False),
    10: ("OUTAGE 100%", 0, 0, True),
    15: ("RECOVERED 200ms", 200, 0, False),
    30: ("DEGRADED 1500ms+80%%loss", 1500, 80, False),
    40: ("PARTIAL 500ms+40%%loss", 500, 40, False),
    50: ("NORMAL 200ms", 200, 0, False),
}

results = []
t0 = time.time()

for sec in range(60):
    if sec in phases:
        name, delay, loss, drop = phases[sec]
        print("\n--- T=%2ds: %s ---" % (sec, name))
        if drop:
            tc(drop_all=True)
        else:
            tc(delay_ms=delay, loss_pct=loss)

    ok, dur, ip = curl_test()
    path = get_path()
    s = "OK" if ok else "FAIL"
    results.append((sec, s, dur, path))
    print("  T=%2ds [%4s] %.1fs via %-6s" % (sec, s, dur, path))

    wait = (t0 + sec + 1) - time.time()
    if wait > 0:
        time.sleep(wait)

# Cleanup
tc()
print()
print("=" * 60)
print("SUMMARY")
print("=" * 60)
total = len(results)
oks = sum(1 for r in results if r[1] == "OK")
print("Success: %d/%d (%d%%)" % (oks, total, oks * 100 // total))
print()

phase_ranges = [
    ("Normal 0-10s", 0, 10),
    ("Outage 10-15s", 10, 15),
    ("Recovery 15-20s", 15, 20),
    ("Normal 20-30s", 20, 30),
    ("Degraded 30-40s", 30, 40),
    ("Partial 40-50s", 40, 50),
    ("Normal 50-60s", 50, 60),
]
for name, s, e in phase_ranges:
    pr = [r for r in results if s <= r[0] < e]
    o = sum(1 for r in pr if r[1] == "OK")
    d = sum(1 for r in pr if r[3] == "DIRECT")
    b = sum(1 for r in pr if r[3] == "B/C")
    avg = sum(r[2] for r in pr) / len(pr) if pr else 0
    print("  %-18s: %d/%d OK  avg=%.1fs  direct=%d B/C=%d" % (name, o, len(pr), avg, d, b))
