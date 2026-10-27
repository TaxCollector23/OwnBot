#!/usr/bin/env python3
"""Independent LAN peer. It has no owner API token or Docker control socket."""
import hashlib
import http.client
import json
import os
import socket
import ssl
import sys

SERVER_IP = "172.28.77.2"


def require(name, passed, status=None):
    if not passed:
        message = "check=" + name
        if status is not None:
            message += " status=" + str(status)
        raise RuntimeError(message)


def main(arg):
    authority = arg["authority"]
    context = ssl.create_default_context(cafile="/lan.cert.pem")
    context.check_hostname = True
    context.verify_mode = ssl.CERT_REQUIRED

    def request(path, method="GET", body=None, token=None, host=None, origin=None):
        connection = http.client.HTTPSConnection(authority, context=context, timeout=4)
        headers = {"Host": host or authority}
        if token:
            headers["Authorization"] = "Bearer " + token
        if origin is not None:
            headers["Origin"] = origin
        data = None
        if body is not None:
            data = json.dumps(body)
            headers["Content-Type"] = "application/json"
        try:
            connection.request(method, path, data, headers)
            response = connection.getresponse()
            raw = response.read()
            return response.status, json.loads(raw) if raw else None
        except (OSError, ssl.SSLError, http.client.HTTPException):
            return 0, None
        finally:
            connection.close()

    op = arg["op"]
    if op == "probe":
        with socket.socket() as local:
            local.settimeout(1)
            isolated = local.connect_ex((SERVER_IP, 8765)) != 0
        conn = http.client.HTTPSConnection(authority, context=context, timeout=4)
        try:
            conn.connect()
            der = conn.sock.getpeercert(binary_form=True)
            conn.request("GET", "/api/status")
            response = conn.getresponse()
            status = response.status
            response.read()
        finally:
            conn.close()
        fingerprint = hashlib.sha256(der).hexdigest()
        expected = arg["fingerprint"].replace(":", "").lower()
        bad_host = request("/api/status", host="attacker.invalid")[0]
        bad_origin = request("/api/status", origin="https://attacker.invalid")[0]
        owner_routes = {
            "owner_access_blocked": request("/api/access", "GET")[0],
            "owner_pairing_blocked": request("/api/devices/pairing", "POST", {})[0],
            "owner_devices_blocked": request("/api/devices", "GET")[0],
            "owner_shutdown_blocked": request("/api/daemon/shutdown", "POST", {})[0],
        }
        # The active bootstrap ticket is created by the local owner fixture and passed in only for this probe.
        # Generate it with the preceding unauthenticated owner-local call for the remote exchange check.
        ticket = arg.get("bootstrap_ticket", "")
        exchange = request("/api/access/bootstrap/exchange", "POST", {"ticket": ticket},
                           origin="https://" + authority)[0]
        result = {"trusted_tls": status == 200, "fingerprint_matches": fingerprint == expected,
                "owner_state_unavailable": not os.path.exists("/state/api.token") and
                not os.path.exists("/state/ownbot.db") and
                not os.path.exists("/state/data/api.token") and
                not os.path.exists("/state/data/ownbot.db"),
                "loopback_isolated": isolated, "bad_host_rejected": bad_host == 403,
                "bad_origin_rejected": bad_origin == 403,
                **{name: route_status == 403 for name, route_status in owner_routes.items()},
                "bootstrap_exchange_blocked": exchange == 403}
        statuses = {"trusted_tls": status, "bad_host_rejected": bad_host,
                    "bad_origin_rejected": bad_origin, "bootstrap_exchange_blocked": exchange,
                    **owner_routes}
        for name, passed in result.items():
            require(name, passed, statuses.get(name))
        return result
    if op == "pair":
        status, data = request("/api/devices/pair", "POST", {"secret": arg["secret"], "name": "CI LAN device"})
        require("pairing", status == 201, status)
        replay = request("/api/devices/pair", "POST", {"secret": arg["secret"], "name": "Replay"})[0]
        require("replay_rejected", replay == 401, replay)
        return {"paired": True, "replay_rejected": True,
                "token": data["token"], "device_id": data["device"]["id"]}
    if op == "authenticated":
        agent_status = request("/api/agents", token=arg["token"])[0]
        require("paired_agent_read", agent_status == 200, agent_status)
        read_status = request("/api/approvals", token=arg["token"])[0]
        require("approval_read", read_status == 200, read_status)
        no_auth = request("/api/approvals", "POST", {"tool": "fixture"})[0]
        require("approval_auth_required", no_auth == 401, no_auth)
        created, approval = request("/api/approvals", "POST", {"tool": "fixture"}, token=arg["token"])
        require("approval_create", created == 201, created)
        decided = request("/api/approvals/" + approval["id"] + "/approve", "POST", {}, token=arg["token"])[0]
        require("approval_decision", decided == 200, decided)
        return {"read_ok": True, "approval_route_auth": no_auth == 401 and decided == 200}
    if op == "revocation":
        revoked = request("/api/agents", token=arg["token"])[0]
        other = request("/api/agents", token=arg["other_token"])[0]
        require("revoked_device_rejected", revoked == 401, revoked)
        require("other_device_works", other == 200, other)
        return {"revoked_rejected": True, "other_device_works": True}
    if op == "after_restart":
        status = request("/api/agents", token=arg["token"])[0]
        require("device_survives_restart", status == 200, status)
        return {"device_survived": True}
    raise ValueError("unknown LAN peer action")


if __name__ == "__main__":
    print(json.dumps(main(json.load(sys.stdin))))
