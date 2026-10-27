#!/usr/bin/env node
// Development installation builds locally; release binaries and signatures are not yet available.
const { spawnSync } = require('node:child_process');
const { mkdirSync } = require('node:fs');
const { join } = require('node:path');
const root = join(__dirname, '..');
const output = join(root, 'bin', process.platform === 'win32' ? 'ownbotd.exe' : 'ownbotd');
mkdirSync(join(root, 'bin'), { recursive: true });
const result = spawnSync('go', ['build', '-o', output, './cmd/ownbotd'], { cwd: root, stdio: 'inherit' });
if (result.error) console.error('OwnBot development installation requires Go 1.24 or later:', result.error.message);
if (result.status !== 0) process.exit(result.status || 1);
