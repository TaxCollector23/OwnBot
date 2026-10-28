#!/usr/bin/env python3
"""Record evidence from the production model capability probe."""
import json
import os
import pathlib
import subprocess
import sys
import threading
import time


def proc_tree_rss_kb(root_pid):
    """Return sampled resident memory for a PID and all current descendants."""
    if not root_pid:
        return None
    processes = {}
    for entry in pathlib.Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            stat = (entry / "stat").read_text()
            tail = stat[stat.rfind(")") + 2:].split()
            ppid = int(tail[1])
            status = (entry / "status").read_text()
            rss = next((int(line.split()[1]) for line in status.splitlines() if line.startswith("VmRSS:")), 0)
            processes[int(entry.name)] = (ppid, rss)
        except (OSError, ValueError, IndexError):
            continue
    included = {int(root_pid)}
    changed = True
    while changed:
        changed = False
        for pid, (ppid, _) in processes.items():
            if ppid in included and pid not in included:
                included.add(pid)
                changed = True
    if int(root_pid) not in processes:
        return None
    return sum(processes[pid][1] for pid in included if pid in processes)


def hardware_summary():
    cpu = "unavailable"
    memory_kb = None
    try:
        for line in pathlib.Path("/proc/cpuinfo").read_text(errors="replace").splitlines():
            if line.startswith("model name"):
                cpu = line.split(":", 1)[1].strip()
                break
        for line in pathlib.Path("/proc/meminfo").read_text().splitlines():
            if line.startswith("MemTotal:"):
                memory_kb = int(line.split()[1])
                break
    except OSError:
        pass
    return cpu, memory_kb


def evidence_path(results_dir, runtime):
    return pathlib.Path(results_dir) / (runtime.replace(".", "-") + ".json")


def baseline(runtime, model):
    cpu, memory_kb = hardware_summary()
    return {
        "runtime": runtime,
        "runtime_version": os.environ.get("RUNTIME_VERSION", "unavailable"),
        "model": model,
        "model_digest": os.environ.get("MODEL_DIGEST", "unavailable"),
        "model_hash": os.environ.get("MODEL_HASH", "unavailable"),
        "quantization": os.environ.get("MODEL_QUANT", "unavailable"),
        "runner_cpu": cpu,
        "runner_ram_total_kb": memory_kb,
        "runtime_peak_rss_kb": None,
        "runtime_pid": os.environ.get("RUNTIME_PID", "unavailable"),
        "probe_elapsed_ms": None,
        "probe_exit_code": None,
        "probe_result": {},
        "pass": False,
        "failure_class": "setup_failure",
        "failure_stage": "runtime_setup",
        "evidence_boundary": "GitHub-hosted Linux CPU runner; not M1/M2/Metal performance or local hardware fit evidence. Capability probe is not quality or hallucination evaluation.",
    }


def write(path, report):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.parent.chmod(0o700)
    path.write_text(json.dumps(report, indent=2) + "\n")
    path.chmod(0o600)


def initialize(runtime, model, results_dir):
    write(evidence_path(results_dir, runtime), baseline(runtime, model))


def probe(runtime, model, base_url, results_dir):
    report = baseline(runtime, model)
    path = evidence_path(results_dir, runtime)
    if path.exists():
        try:
            report.update(json.loads(path.read_text()))
        except (OSError, json.JSONDecodeError):
            pass
    helper = os.environ.get("MODEL_PROBE", "")
    if not helper:
        report["error_type"] = "missing_probe_binary"
        write(path, report)
        return False

    report.update(baseline(runtime, model))
    report["failure_stage"] = "probe"
    report["failure_class"] = "probe_or_transport_failure"
    started = time.monotonic()
    root_pid = os.environ.get("RUNTIME_PID", "")
    stop = threading.Event()
    peak = [None]

    def sample_rss():
        while not stop.is_set():
            current = proc_tree_rss_kb(root_pid)
            if current is not None:
                peak[0] = max(peak[0] or 0, current)
            stop.wait(0.25)

    sampler = threading.Thread(target=sample_rss, daemon=True)
    sampler.start()
    try:
        completed = subprocess.run(
            [helper, "--runtime", runtime, "--model", model, "--base-url", base_url],
            check=False, capture_output=True, text=True, timeout=540,
        )
        report["probe_exit_code"] = completed.returncode
        report["failure_class"] = "probe_or_transport_failure"
        try:
            probe_result = json.loads(completed.stdout.strip().splitlines()[-1])
            report["probe_result"] = probe_result.get("probe", {})
            report["probe_elapsed_ms"] = probe_result.get("elapsed_ms")
            report["failure_class"] = probe_result.get("failure_class", report["failure_class"])
            report["failure_stage"] = probe_result.get("failure_stage", report["failure_stage"])
            if probe_result.get("error_type"):
                report["error_type"] = probe_result["error_type"]
            report["pass"] = completed.returncode == 0 and probe_result.get("pass") is True
        except (IndexError, json.JSONDecodeError, AttributeError):
            report["error_type"] = "invalid_probe_output"
        report["probe_process_elapsed_ms"] = round((time.monotonic() - started) * 1000)
    except subprocess.TimeoutExpired:
        report["error_type"] = "probe_timeout"
        report["failure_class"] = "probe_or_transport_failure"
    except OSError:
        report["error_type"] = "probe_process_error"
        report["failure_class"] = "setup_failure"
    finally:
        stop.set()
        sampler.join(timeout=2)
        if peak[0] is not None:
            report["runtime_peak_rss_kb"] = peak[0]
    if report["pass"]:
        report["failure_class"] = "none"
        report["failure_stage"] = "complete"
    write(path, report)
    print(json.dumps({"runtime": runtime, "model": model, "pass": report["pass"], "probe_elapsed_ms": report["probe_elapsed_ms"], "failure_stage": report["failure_stage"]}, indent=2))
    return report["pass"]


def main():
    if len(sys.argv) < 2:
        raise SystemExit("usage: run.py init RUNTIME MODEL RESULTS_DIR | probe RUNTIME MODEL BASE_URL RESULTS_DIR")
    if sys.argv[1] == "init" and len(sys.argv) == 5:
        _, _, runtime, model, results_dir = sys.argv
        initialize(runtime, model, results_dir)
        return 0
    if sys.argv[1] == "probe" and len(sys.argv) == 6:
        _, _, runtime, model, base_url, results_dir = sys.argv
        return 0 if probe(runtime, model, base_url, results_dir) else 1
    raise SystemExit("usage: run.py init RUNTIME MODEL RESULTS_DIR | probe RUNTIME MODEL BASE_URL RESULTS_DIR")


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        # Keep workflow logs and uploaded evidence free of runtime response content.
        print("FAIL: local model lab stopped (" + type(error).__name__ + ")", file=sys.stderr)
        raise SystemExit(1)
