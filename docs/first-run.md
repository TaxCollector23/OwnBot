# First run

The intended install-and-setup flow is:

```sh
npm install -g ownbot
ownbot setup
```

The npm package and matching GitHub release assets have not been published yet. The first command becomes available after that release; the development build steps are in the [README](../README.md).

## Interactive setup

Run `ownbot setup` in a terminal. Use ↑/↓ to choose a runtime or model, Enter to confirm, Space to toggle a permission, and Escape to cancel the current choice. Setup scans the host's CPU, physical RAM, NVIDIA VRAM when `nvidia-smi` is available, local runtimes, and free disk space. Apple Silicon is reported as unified memory; unavailable measurements are shown as unavailable rather than treated as zero or as total RAM.

The runtime menu includes Ollama, LM Studio, and Jan. `llama.cpp` and vLLM remain advanced choices when their localhost APIs are running. OwnBot does not install third-party runtimes. If a runtime is missing, install it yourself from its official site and rerun setup. When Ollama or LM Studio is installed but stopped, setup can start its local service.

Jan can expose cloud model IDs alongside local models. OwnBot accepts only a Jan model that appears in the local API and matches a `model.yml` entry plus non-empty weight files under Jan's local `llamacpp/models` or `mlx/models` directory. If Jan uses a custom data directory, set `JAN_DATA_DIR` to it. Jan's local API can require a key; setup masks the key while typing and stores it only in the private OwnBot state database after you save the model. The key can also be supplied through `OWNBOT_JAN_API_KEY` for non-interactive setup.

## Model choice and downloads

The recommendation selects the largest maintained shortlist candidate that fits current available-memory telemetry and a 20% free-disk reserve. It displays download size, a conservative memory estimate, and the reason. This is a fit estimate, not a quality ranking or a claim that the model is optimal. Discrete VRAM is added to available RAM only when the host is not using Apple Silicon unified memory.

- **Ollama:** setup can pull the recommended Qwen3 model and displays pull progress. Ctrl+C cancels the request; completed Ollama layers may remain available for a retry. Its built-in shortlist ranges from `qwen3:0.6b` through `qwen3:8b`.
- **LM Studio:** setup uses the documented `lms get` and `lms load` commands for its small Qwen3 and Granite shortlist. It still checks local model metadata and capability-probes the selected model before creating an agent.
- **Jan:** Jan documents model listing and inference APIs but no model-download API. Download and load a local model in Jan Hub, return to the terminal, and confirm the local check. See [Jan model management](https://docs.jan.ai/desktop/manage-models).
- **llama.cpp and vLLM:** select only models already served by their loopback APIs; setup does not download models for these advanced runtimes.

Every selected model runs OwnBot's existing capability probe. A failed probe shows the failed checks and offers another installed or shortlisted model. Setup creates no agent until one model passes all required checks.

## Permissions and agent

The permission checklist is populated from the host's actual connected tools. It groups tools as read-only, data-changing, or irreversible. The default selection is empty. If no tools are connected, the agent starts in chat-only mode. OwnBot does not grant filesystem, shell, or general network access through this checklist.

Selected grants are saved on the agent. They do not bypass server-side runtime policies or approval requirements. Setup creates or reuses the named agent and model configuration, so rerunning it does not make duplicate entries. No task is run during setup.

## Start and pair a device

After the capability check, setup starts OwnBot with LAN access enabled and prints a one-use pairing QR and URL. The host continues to run inference and tools; the paired browser is a dashboard. A paired browser can select an enabled agent, submit tasks, review task history, approve or deny supported actions, and forget its local paired token. Host-side device revocation remains available with `ownbot daemon devices --revoke DEVICE_ID`.

LAN HTTPS uses a host-local root certificate. Setup prints its SHA-256 fingerprint and the public certificate file path, usually `~/.ownbot/lan.cert.cer`. Transfer only this public certificate to the phone using a trusted method, compare its fingerprint with the terminal, and install it before opening the QR link. Do not transfer `lan.key.pem`, and do not bypass a browser certificate warning. The exact iOS and Android steps and current physical-device test status are in the [LAN guide](lan-access.md).

The QR contains a short-lived, one-use pairing secret; it never contains the permanent host API token. The paired browser stores its device token in `sessionStorage`, which survives reloads in that tab but is cleared when that tab session ends.

## Non-interactive setup

For scripts or terminals without a TTY, supply an installed model explicitly. For example:

```sh
ownbot setup --yes \
  --provider ollama \
  --base-url http://127.0.0.1:11434/v1 \
  --model qwen3:1.7b \
  --agent-name Assistant
```

Use `--config setup.toml` to provide `[model]` and `[agent]` fields. `OWNBOT_JAN_API_KEY` and `LM_API_TOKEN` can supply the optional local runtime key without putting it in command arguments. With the default daemon URL, successful non-interactive setup also enables LAN access and prints a pairing QR. For a custom daemon URL, setup preserves the existing flow and asks you to start LAN access explicitly.

The non-interactive path does not silently download a model or enable tools. It reuses the discovered installed model, capability probe, and the same no-task-run behavior.

## What is not yet verified

Automated tests cover terminal key handling, model resource selection, mocked download errors/cancellation, local Jan-file matching, permission persistence, one-use QR content, and the browser task surface. Real model downloads, host-specific model performance, macOS/Linux login-service behavior on a clean machine, and certificate installation plus browser pairing on a real iPhone, Android device, or second laptop still need physical verification. See [STATUS.md](../STATUS.md).
