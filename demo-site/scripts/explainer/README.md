# OwnBot whiteboard explainer

Run **Actions → Record sandbox explainer**. The disposable VM builds the actual CLI and daemon, synthesizes the public script with Edge TTS's Aria voice, records documentation, terminal output, drawn diagrams, and a real dashboard approval against the explicitly simulated test model. FFmpeg adds voice and burned-in captions. Download the MP4 and SRT artifact; no video is embedded into the docs site.

The test requires zero fixture writes while approval is pending and exactly one after approval. This is harness evidence, not model-quality evidence. Voice synthesis sends only the public narration to Microsoft's online speech service; it needs no API key and installs no local weights.

References used:
- [Requested record-demo skill / aidemo authoring guide](https://github.com/tandryukha/aidemo/blob/stable/docs/AUTHORING.md): narration-first timing and post-render review.
- [Benji Taylor's stageshow](https://github.com/benjitaylor/skills/tree/main/stageshow): quiet framing and one causal action at a time. Diagrams are illustrations; dashboard footage is a real browser capture.
- [Narrated demo skill](https://github.com/Hmache/demo): timed voice, captions, actual browser interactions.
- [Edge TTS](https://github.com/rany2/edge-tts): neural narration without a provider key.

This repository uses a small Playwright/FFmpeg recorder with the existing test fixtures, rather than installing the full third-party demo engines. The requested skills were inspected and installed locally under `.codex/skills` and are available on subsequent turns.
