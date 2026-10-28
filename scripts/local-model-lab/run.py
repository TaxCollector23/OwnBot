#!/usr/bin/env python3
"""Run OwnYourBot's real model capability probe against one local runtime."""
import json
import os
import pathlib
import subprocess
import sys
import time
import urllib.request


def run(args, *, check=True, capture=True):
    return subprocess.run(args, check=check, text=True,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.PIPE if capture else None)


def wait(url, timeout=300):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as response:
                if response.status < 500:
                    return
        except Exception:
            time.sleep(1)
    raise RuntimeError("runtime readiness timeout")


def main():
    if len(sys.argv) != 5:
        raise SystemExit("usage: run.py ollama|llama.cpp MODEL BASE_URL RESULTS_DIR")
    provider, model, base_url, results_dir = sys.argv[1:]
    if provider not in ("ollama", "llama.cpp"):
        raise SystemExit("unsupported runtime")
    root = pathlib.Path(results_dir)
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    root.chmod(0o700)
    data_dir = root / "ownbot-state"
    evidence = {
        "runtime": provider,
        "runtime_version": os.environ.get("RUNTIME_VERSION", "unavailable"),
        "model": model,
        "model_digest": os.environ.get("MODEL_DIGEST", "unavailable"),
        "model_hash": os.environ.get("MODEL_HASH", "unavailable"),
        "quantization": os.environ.get("MODEL_QUANT", "unavailable"),
        "probe_elapsed_ms": None,
        "probe_exit_code": None,
        "probe_raw_stdout": "",
        "probe_result": {},
        "pass": False,
        "failure_class": "setup_failure",
        "failure_stage": "daemon_start",
        "evidence_boundary": "GitHub-hosted Linux CPU runner; not M1/M2/Metal performance or local hardware fit evidence. Capability probe is not quality or hallucination evaluation.",
    }
    daemon = None
    try:
        daemon = subprocess.Popen([os.environ["OWNBOTD"], "--data-dir", str(data_dir), "--listen", "127.0.0.1:18765"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        evidence["failure_stage"] = "daemon_readiness"
        wait("http://127.0.0.1:18765/api/status", 45)
        cli = os.environ["OWNBOT"]
        evidence["failure_stage"] = "model_registration"
        add = run([cli, "model", "add", "--data-dir", str(data_dir), "--provider", "ollama" if provider == "ollama" else "openai_compatible", "--base-url", base_url, "--name", model, "--model", model], check=False)
        if add.returncode:
            evidence["registration_exit_code"] = add.returncode
            evidence["failure_class"] = "model_registration_failure"
            raise RuntimeError("model registration failed")
        evidence["failure_stage"] = "model_registration_response"
        config = json.loads(add.stdout)
        evidence["model_config_id"] = config.get("id", "unavailable")
        evidence["failure_stage"] = "probe_request"
        started = time.monotonic()
        probe = run([cli, "model", "probe", "--data-dir", str(data_dir), config["id"]], check=False)
        elapsed_ms = round((time.monotonic() - started) * 1000)
        evidence["probe_elapsed_ms"] = elapsed_ms
        evidence["probe_exit_code"] = probe.returncode
        evidence["probe_raw_stdout"] = probe.stdout
        evidence["failure_class"] = "probe_or_transport_failure"
        try:
            start = probe.stdout.find("{")
            payload, _ = json.JSONDecoder().raw_decode(probe.stdout[start:]) if start >= 0 else ({}, 0)
        except Exception:
            payload = {"parse_error": True, "exit_code": probe.returncode}
        evidence["probe_result"] = payload
        checks = payload.get("probe", {})
        required = ("json_output", "single_tool", "tool_choice", "argument_accuracy")
        passed = probe.returncode == 0 and all(checks.get(name) is True for name in required) and checks.get("restricted") is False
        if probe.returncode == 0:
            evidence["failure_class"] = "model_capability_failure"
        os_release = pathlib.Path("/etc/os-release").read_text() if pathlib.Path("/etc/os-release").exists() else "unavailable"
        cpu = pathlib.Path("/proc/cpuinfo").read_text(errors="replace") if pathlib.Path("/proc/cpuinfo").exists() else "unavailable"
        mem = pathlib.Path("/proc/meminfo").read_text(errors="replace") if pathlib.Path("/proc/meminfo").exists() else "unavailable"
        peak_rss_kb = None
        runtime_pid = os.environ.get("RUNTIME_PID", "")
        if runtime_pid:
            status_path = pathlib.Path("/proc") / runtime_pid / "status"
            if status_path.exists():
                for line in status_path.read_text(errors="replace").splitlines():
                    if line.startswith("VmHWM:"):
                        peak_rss_kb = int(line.split()[1])
                        break
        evidence.update({
            "runner_os": os_release,
            "runner_cpuinfo": cpu,
            "runner_meminfo": mem,
            "runtime_pid": runtime_pid or "unavailable",
            "runtime_peak_rss_kb": peak_rss_kb,
            "pass": passed,
            "failure_class": "none" if passed else evidence["failure_class"],
            "failure_stage": "complete" if passed else "capability_checks",
        })
        print(json.dumps({"runtime": provider, "model": model, "pass": passed, "probe_elapsed_ms": elapsed_ms, "probe": payload.get("probe", payload)}, indent=2))
        if not passed:
            evidence["failure_class"] = "model_capability_failure" if probe.returncode == 0 else "probe_or_transport_failure"
    except Exception as error:
        evidence["error_type"] = type(error).__name__
        print("FAIL: local model lab stopped at " + evidence["failure_stage"] + " (" + type(error).__name__ + ")", file=sys.stderr)
    finally:
        (root / (provider.replace(".", "-") + ".json")).write_text(json.dumps(evidence, indent=2) + "\n")
        if daemon:
            daemon.terminate()
            try:
                daemon.wait(timeout=8)
            except subprocess.TimeoutExpired:
                daemon.kill()
    if not evidence["pass"]:
        raise SystemExit(1)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        # Never dump process output on failure; model/server output may be unbounded.
        print("FAIL: local model lab stopped (" + type(error).__name__ + ")", file=sys.stderr)
        raise SystemExit(1)
