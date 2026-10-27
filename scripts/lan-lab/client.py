#!/usr/bin/env python3
"""Independent LAN peer. It has no owner API token or Docker control socket."""
import hashlib
import http.client
import json
import os
import socket
import ssl
import sys


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
            isolated = local.connect_ex(("127.0.0.1", 8765)) != 0
        with http.client.HTTPSConnection(authority, context=context, timeout=4) as conn:
            conn.request("GET", "/api/status")
            response = conn.getresponse()
            der = conn.sock.getpeercert(binary_form=True)
            status = response.status
            response.read()
        fingerprint = hashlib.sha256(der).hexdigest()
        expected = arg["fingerprint"].replace(":", "").lower()
        bad_host = request("/api/status", host="attacker.invalid")[0]
        bad_origin = request("/api/status", origin="https://attacker.invalid")[0]
        blocked = all(request(path, method, {})[0] == 403 for path, method in [
            ("/api/access", "GET"), ("/api/devices/pairing", "POST"),
            ("/api/devices", "GET"), ("/api/daemon/shutdown", "POST")])
        # The active bootstrap ticket is created by the local owner fixture and passed in only for this probe.
        # Generate it with the preceding unauthenticated owner-local call for the remote exchange check.
        ticket = arg.get("bootstrap_ticket", "")
        exchange = request("/api/access/bootstrap/exchange", "POST", {"ticket": ticket},
                           origin="https://" + authority)[0]
        return {"trusted_tls": status == 200, "fingerprint_matches": fingerprint == expected,
                "owner_state_unavailable": not os.path.exists("/state/api.token") and
                not os.path.exists("/state/ownbot.db"),
                "loopback_isolated": isolated, "bad_host_rejected": bad_host == 403,
                "bad_origin_rejected": bad_origin == 403, "owner_routes_blocked": blocked,
                "bootstrap_exchange_blocked": exchange == 403}
    if op == "pair":
        status, data = request("/api/devices/pair", "POST", {"secret": arg["secret"], "name": "CI LAN device"})
        assert status == 201
        replay = request("/api/devices/pair", "POST", {"secret": arg["secret"], "name": "Replay"})[0]
        return {"paired": True, "replay_rejected": replay == 401,
                "token": data["token"], "device_id": data["device"]["id"]}
    if op == "authenticated":
        assert request("/api/status", token=arg["token"])[0] == 200
        assert request("/api/approvals", token=arg["token"])[0] == 200
        no_auth = request("/api/approvals", "POST", {"tool": "fixture"})[0]
        created, approval = request("/api/approvals", "POST", {"tool": "fixture"}, token=arg["token"])
        assert created == 201
        decided = request("/api/approvals/" + approval["id"] + "/approve", "POST", {}, token=arg["token"])[0]
        return {"read_ok": True, "approval_route_auth": no_auth == 401 and decided == 200}
    if op == "revocation":
        return {"revoked_rejected": request("/api/status", token=arg["token"])[0] == 401,
                "other_device_works": request("/api/status", token=arg["other_token"])[0] == 200}
    if op == "after_restart":
        return {"device_survived": request("/api/status", token=arg["token"])[0] == 200}
    raise ValueError("unknown LAN peer action")


if __name__ == "__main__":
    print(json.dumps(main(json.load(sys.stdin))))
