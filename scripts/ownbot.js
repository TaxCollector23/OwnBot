#!/usr/bin/env node
const { spawn } = require('node:child_process');
const { join } = require('node:path');
const binary = join(__dirname, '..', 'bin', process.platform === 'win32' ? 'ownbot.exe' : 'ownbot');
const child = spawn(binary, process.argv.slice(2), { stdio: 'inherit', env: process.env });
child.on('error', error => { console.error('Could not start ownbot CLI:', error.message); process.exitCode = 1; });
child.on('exit', (code, signal) => { if (signal) process.kill(process.pid, signal); else process.exitCode = code; });
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => child.kill(signal));
