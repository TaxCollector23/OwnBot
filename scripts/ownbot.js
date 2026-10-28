#!/usr/bin/env node
const { spawn } = require('node:child_process');
const { existsSync, readFileSync } = require('node:fs');
const { join } = require('node:path');
const bin = join(__dirname, '..', 'bin');
const suffix = process.platform === 'win32' ? '.exe' : '';
let binary = join(bin, 'ownbot' + suffix);
const marker = join(bin, 'current.json');
if (existsSync(marker)) {
  try {
    const { version } = JSON.parse(readFileSync(marker, 'utf8'));
    if (!/^[0-9A-Za-z.+-]+$/.test(version)) throw new Error('invalid active version');
    binary = join(bin, 'releases', version, 'ownbot' + suffix);
  } catch (error) {
    console.error('Could not read the active OwnBot release:', error.message);
    process.exit(1);
  }
}
const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit', env: process.env });
child.on('error', error => { console.error('Could not start ownbot CLI:', error.message); process.exitCode = 1; });
child.on('exit', (code, signal) => { if (signal) process.kill(process.pid, signal); else process.exitCode = code; });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
