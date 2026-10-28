# Hardware inspection

Run `ownbot inspect hardware` for the host CPU architecture, physical RAM totals and available RAM when the OS exposes them, GPU/VRAM telemetry when `nvidia-smi` is available, and local models discovered from supported local runtimes. Apple Silicon is reported as using unified system memory; GPU allocation is not separately measured.

With a running daemon, the report also includes saved model capability probes. Saved probes have no latency data, so they are not used to rank models.

`ownbot inspect hardware --benchmark` explicitly runs the daemon's capability probe against configured local models that are also found in their provider's installed-model list. It records elapsed probe latency, and can be limited to one exact model name or config ID with `--model NAME`. Each call is bounded by `--max-probe-seconds` (1 to 600; default 120). No models are downloaded and no hosted provider endpoint or key is used. The capability probe makes small local inference requests, so it uses model runtime resources.

The fastest model that passes every recorded capability check in that invocation is identified as a task-agnostic capability/latency result. It is not an accuracy, answer-quality, or hallucination score. Model memory requirements are unknown unless the runtime supplies explicit metadata; this report does not infer them from model names or parameter counts. Tokens per second is omitted because the capability probe response does not report token counts.

RAM and VRAM are separate where telemetry exists. Linux uses `/proc/meminfo`; macOS uses `sysctl` and estimates available memory from free, inactive, and speculative pages in `vm_stat`. NVIDIA VRAM comes from `nvidia-smi`. Missing telemetry is reported as unavailable rather than as zero. Other GPU vendors, GPU utilization, swap pressure, and per-model memory use are not measured in this version.

## Observed model checks

| Runtime | Model | Quantization | Capability checks | Probe time | Runtime peak RSS |
| --- | --- | --- | --- | --- | --- |
| Ollama 0.40.12 | qwen3:1.7b | Q4_K_M | 4/4 passed | 44.0 seconds | 1.92 GiB |
| llama.cpp ac8e1694 | Qwen3-1.7B GGUF | Q8_0 artifact label; runtime metadata unavailable | 4/4 passed | 30.5 seconds | 2.07 GiB |

Measured on separate GitHub-hosted Linux CPU runners with 15.6 GiB RAM: Intel Xeon 6973P-C for [Ollama](https://github.com/TaxCollector23/OwnBot/actions/runs/40738088838), AMD EPYC 9V45 for [llama.cpp](https://github.com/TaxCollector23/OwnBot/actions/runs/40741788083). The hosts differ, so these timings cannot rank the runtimes. RSS is sampled process memory, not a minimum RAM requirement. This checks JSON, one tool call, tool choice, and arguments; it does not measure answer quality or hallucinations. M2, FreeBSD, larger models, and GPU performance remain unmeasured. Always probe the installed model on the target host.
