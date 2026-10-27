#!/usr/bin/env python3
"""Fixture owner actions, always executed inside the server container."""
import json
import sys
import urllib.request

BASE = "http://127.0.0.1:8765"


def request(path, method="GET", body=None, auth=True):
    token = open("/state/api.token", encoding="ascii").read().strip()
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if auth:
        req.add_header("Authorization", "Bearer " + token)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=4) as response:
            raw = response.read()
            return response.status, json.loads(raw) if raw else None
    except Exception as error:
        if getattr(error, "code", None):
            raw = error.read()
            return error.code, json.loads(raw) if raw else None
        raise


def main(arg):
    op = arg["op"]
    if op == "health":
        try:
            return {"ready": request("/api/status", auth=False)[0] == 200}
        except Exception:
            return {"ready": False}
    if op == "access":
        status, data = request("/api/access")
        assert status == 200
        status, bootstrap = request("/api/access/bootstrap", "POST", {})
        assert status == 201
        return {"fingerprint": data["certificate_fingerprint"],
                "bootstrap_ticket": bootstrap["ticket"]}
    if op == "pair":
        status, data = request("/api/devices/pairing", "POST", {})
        assert status == 201
        return {"secret": data["pairing_url"].split("#pair=", 1)[1]}
    if op == "revoke":
        status, _ = request("/api/devices/" + arg["device_id"], "DELETE", {})
        assert status == 200
        return {"revoked": True}
    if op == "shutdown":
        status, _ = request("/api/daemon/shutdown", "POST", {})
        assert status == 202
        return {"shutdown": True}
    raise ValueError("unknown fixture owner action")


if __name__ == "__main__":
    print(json.dumps(main(json.load(sys.stdin))))
