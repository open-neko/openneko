#!/bin/sh
# neko-graphjin entrypoint. Templates /seed/neko.yml into /config/dev.yml
# at every container start, substituting the database password from
# /openneko-config/config.json (mounted from the openneko-config volume,
# which the worker entrypoint and the /setup wizard both write).
# Falls back to NEKO_PG_PASSWORD when config.json is missing.
#
# Doing this on every start (rather than once at image build time) is
# what makes password rotation just work: the operator changes their
# password via /setup, the wizard rewrites config.json, and the next
# `docker compose restart neko-graphjin` picks up the new value.
set -eu

node - <<'JS'
const fs = require('fs');

let pw = process.env.NEKO_PG_PASSWORD || 'secret';
let source = 'NEKO_PG_PASSWORD env';

try {
  const raw = fs.readFileSync('/openneko-config/config.json', 'utf8');
  const cfg = JSON.parse(raw);
  if (cfg && cfg.pg && typeof cfg.pg.password === 'string' && cfg.pg.password.length > 0) {
    pw = cfg.pg.password;
    source = 'config.json';
  }
} catch (err) {
  console.warn(
    `[neko-graphjin] config.json unreadable (${err.message}); using ${source}`,
  );
}

const seed = fs.readFileSync('/seed/neko.yml', 'utf8');
const templated = seed.replace(/^(\s*)password:.*$/m, `$1password: ${pw}`);

fs.mkdirSync('/config', { recursive: true });
fs.writeFileSync('/config/dev.yml', templated);
console.log(`[neko-graphjin] templated /config/dev.yml from ${source}`);
JS

exec graphjin "$@"
