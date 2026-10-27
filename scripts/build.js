#!/usr/bin/env node
// Development installation builds locally; release binaries and signatures are not yet available.
const { spawnSync } = require('node:child_process');
const { mkdirSync } = require('node:fs');
const { join } = require('node:path');
const root = join(__dirname, '..');
mkdirSync(join(root, 'bin'), { recursive: true });
for (const binary of ['ownbotd', 'ownbot']) {
const output = join(root, 'bin', binary + (process.platform === 'win32' ? '.exe' : ''));
const result = spawnSync('go', ['build', '-trimpath', '-o', output, './cmd/' + binary], { cwd: root, stdio: 'inherit' });
if (result.error) console.error('OwnYourBot alpha installation requires Go 1.24 or later:', result.error.message);
if (result.status !== 0) process.exit(result.status || 1);
}
