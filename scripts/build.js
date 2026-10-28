#!/usr/bin/env node
// npm install consumes only signed, package-version-matched prebuilt releases.
// A local source build is available only when explicitly requested.
'use strict';

const { spawnSync } = require('node:child_process');
const { mkdirSync, mkdtempSync, readFileSync, rmSync } = require('node:fs');
const { join } = require('node:path');
const root = join(__dirname, '..');

if (process.env.OWNBOT_BUILD_FROM_SOURCE === '1') {
  const bin = join(root, 'bin');
  mkdirSync(bin, { recursive: true, mode: 0o700 });
  const stage = mkdtempSync(join(bin, '.source-'));
  try {
    const suffix = process.platform === 'win32' ? '.exe' : '';
    for (const binary of ['ownbotd', 'ownbot']) {
      const output = join(stage, binary + suffix);
      const result = spawnSync('go', ['build', '-trimpath', '-o', output, './cmd/' + binary], { cwd: root, stdio: 'inherit' });
      if (result.error) {
        console.error('Could not run the explicit Go source build:', result.error.message);
        process.exit(1);
      }
      if (result.status !== 0) process.exit(result.status || 1);
    }
    const { installBinaries } = require('./release-install');
    installBinaries(root, `source-${Date.now()}`, new Map([
      ['ownbot' + suffix, readFileSync(join(stage, 'ownbot' + suffix))],
      ['ownbotd' + suffix, readFileSync(join(stage, 'ownbotd' + suffix))],
    ]));
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
  process.stdout.write('Built OwnBot from source as requested by OWNBOT_BUILD_FROM_SOURCE=1.\n');
} else {
  const { installPackageVersion } = require('./release-install');
  const pkg = require(join(root, 'package.json'));
  installPackageVersion(root, pkg.version).catch(error => {
    console.error(`Could not install signed OwnBot ${pkg.version}: ${error.message}`);
    console.error('For an explicit Go source build, set OWNBOT_BUILD_FROM_SOURCE=1.');
    process.exitCode = 1;
  });
}
