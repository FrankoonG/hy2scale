#!/usr/bin/env python3
"""Simple SSH helper to execute commands on remote host."""
import sys
import paramiko

def run_remote(cmd):
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect('10.130.32.40', username='sshd', password='test1234', timeout=10)
    stdin, stdout, stderr = client.exec_command(cmd, timeout=30)
    out = stdout.read().decode('utf-8', errors='replace')
    err = stderr.read().decode('utf-8', errors='replace')
    rc = stdout.channel.recv_exit_status()
    client.close()
    if out:
        print(out, end='')
    if err:
        print(err, end='', file=sys.stderr)
    sys.exit(rc)

if __name__ == '__main__':
    run_remote(' '.join(sys.argv[1:]))
