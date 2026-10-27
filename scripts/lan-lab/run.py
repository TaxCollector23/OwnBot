#!/usr/bin/env python3
"""Run the real OwnYourBot LAN HTTPS path between isolated Docker peers."""
import json
import os
import subprocess
import sys
import tempfile
import time
import uuid


RUN_ID = uuid.uuid4().hex[:12]
NETWORK = "ownbot-lan-lab-" + RUN_ID
SERVER = "ownbot-lan-server-" + RUN_ID
CLIENT = "ownbot-lan-client-" + RUN_ID
STATE_VOLUME = "ownbot-lan-state-" + RUN_ID
CLIENT_VOLUME = "ownbot-lan-client-state-" + RUN_ID
IMAGE = "python:3.12-alpine"
SERVER_IP = "172.28.77.2"
AUTHORITY = SERVER_IP + ":8766"
ROOT = os.path.dirname(os.path.abspath(__file__))
stage = "initialization"


def docker(*args, check=True, input_text=None):
    return subprocess.run(["docker", *args], text=True, input=input_text,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          check=check)


def container_python(name, script, payload=None, check=True):
    result = docker("exec", "-i", name, "python3", "/lab/" + script,
                    check=check, input_text=json.dumps(payload or {}))
    if result.returncode:
        raise RuntimeError("lab probe failed")
    return json.loads(result.stdout)


def wait_server(up, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            state = container_python(SERVER, "owner.py", {"op": "health"})
            if state["ready"] == up:
                return
        except Exception:
            if not up:
                return
        time.sleep(.2)
    raise RuntimeError("LAN lab daemon did not reach expected state")


def clean():
    for name in (CLIENT, SERVER):
        docker("rm", "-f", name, check=False)
    docker("volume", "rm", "-f", STATE_VOLUME, CLIENT_VOLUME, check=False)
    docker("network", "rm", NETWORK, check=False)


def main(binary):
    global stage
    clean()
    cert_path = None
    stage = "network setup"
    try:
        docker("network", "create", "--driver", "bridge", "--subnet", "172.28.77.0/24", NETWORK)
        docker("volume", "create", STATE_VOLUME)
        docker("volume", "create", CLIENT_VOLUME)
        stage = "server startup"
        docker("run", "-d", "--name", SERVER, "--network", NETWORK, "--ip", SERVER_IP,
               "-v", STATE_VOLUME + ":/state",
               "-v", ROOT + ":/lab:ro",
               "-v", os.path.abspath(binary) + ":/usr/local/bin/ownbotd:ro",
               IMAGE, "sh", "-c",
               "while :; do /usr/local/bin/ownbotd --data-dir /state/data --listen 127.0.0.1:8765 --lan-address " + AUTHORITY + " >/dev/null 2>&1; sleep 1; done")
        stage = "daemon readiness"
        wait_server(True)
        stage = "owner fixture and public certificate setup"
        access = container_python(SERVER, "owner.py", {"op": "access"})
        fd, cert_path = tempfile.mkstemp(prefix="ownbot-lan-lab-", suffix=".pem")
        os.close(fd)
        docker("cp", SERVER + ":/state/data/lan.cert.pem", cert_path)
        docker("run", "-d", "--name", CLIENT, "--network", NETWORK, "--ip", "172.28.77.3",
               "-v", cert_path + ":/lan.cert.pem:ro",
               "-v", CLIENT_VOLUME + ":/client-state",
               "-v", ROOT + ":/lab:ro", IMAGE, "sh", "-c", "sleep 900")
        stage = "TLS and route-hardening probes"
        result = container_python(CLIENT, "client.py", {
            "op": "probe", "authority": AUTHORITY,
            "fingerprint": access["fingerprint"], "bootstrap_ticket": access["bootstrap_ticket"],
        })
        assert result["trusted_tls"] and result["fingerprint_matches"]
        assert result["loopback_isolated"]
        assert result["owner_state_unavailable"]
        assert result["bad_host_rejected"] and result["bad_origin_rejected"]
        assert result["owner_routes_blocked"] and result["bootstrap_exchange_blocked"]
        print("PASS: bridge client reached real TLS LAN listener; certificate trusted and pinned")
        print("PASS: loopback isolation, Host/Origin rejection, owner/session/bootstrap routes blocked")

        stage = "one-use pairing and approval probes"
        created = container_python(SERVER, "owner.py", {"op": "pair"})
        paired = container_python(CLIENT, "client.py", {
            "op": "pair", "authority": AUTHORITY, "secret": created["secret"],
        })
        assert paired["paired"] and paired["replay_rejected"]
        auth = container_python(CLIENT, "client.py", {
            "op": "authenticated", "authority": AUTHORITY,
            "token": paired["token"], "device_id": paired["device_id"],
        })
        assert auth["read_ok"] and auth["approval_route_auth"]
        print("PASS: one-use pairing, replay rejection, paired reads and approval route authorization")

        stage = "revocation probes"
        second = container_python(SERVER, "owner.py", {"op": "pair"})
        paired2 = container_python(CLIENT, "client.py", {
            "op": "pair", "authority": AUTHORITY, "secret": second["secret"],
        })
        container_python(SERVER, "owner.py", {"op": "revoke", "device_id": paired["device_id"]})
        revoke = container_python(CLIENT, "client.py", {
            "op": "revocation", "authority": AUTHORITY,
            "token": paired["token"], "other_token": paired2["token"],
        })
        assert revoke["revoked_rejected"] and revoke["other_device_works"]
        print("PASS: revoking one device leaves the second paired device active")

        stage = "daemon restart and persistence probes"
        container_python(SERVER, "owner.py", {"op": "shutdown"})
        wait_server(False)
        wait_server(True)
        restart = container_python(CLIENT, "client.py", {
            "op": "after_restart", "authority": AUTHORITY,
            "token": paired2["token"],
        })
        assert restart["device_survived"]
        print("PASS: paired device persisted and authenticated after daemon restart")
        print("Evidence: two isolated Docker bridge containers, server 172.28.77.2, client 172.28.77.3")
        print("Limit: this validates container networking and Python HTTPS behavior, not physical devices, browsers, Wi-Fi, or hardware quality")
    finally:
        clean()
        if cert_path:
            os.unlink(cert_path)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: run.py /path/to/ownbotd")
    try:
        main(sys.argv[1])
    except Exception as exc:
        # Avoid printing command output: it can contain ephemeral lab credentials.
        print("FAIL: isolated LAN lab stopped during " + stage + " (" + type(exc).__name__ + ")", file=sys.stderr)
        raise SystemExit(1) from exc
