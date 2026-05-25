#!/usr/bin/env bash
# Install the Telegram channel plugin on neko-vm during deploy.
#
# Channel plugins are not yet first-class in the Go `openneko install` path
# (its capability model predates `capabilities.channel`), so this fetches the
# published npm package into the plugin dir and registers the channel manifest
# entry directly — the TS worker fully supports capabilities.channel and picks
# up manifest+secret changes within ~3s via its poll fallback.
#
# Invoked gated + non-fatal from .github/workflows/deploy.yml. No-ops cleanly
# when TELEGRAM_BOT_TOKEN is absent. Reads OPENNEKO_PLUGIN_INSTALL_DIR +
# OPENNEKO_PLUGINS_MANIFEST_PATH from the deploy environment.
set -uo pipefail

PKG="@open-neko/channel-telegram"
VER="0.2.0"
INTEGRITY="sha512-6DCDiVh+CMU6wCuyp3N2RMIhpehs/C0vC2nP08XzFzo/8uK1GLO0YpN4wXYp3RUMcrYBiOvCLyx4+gbuNE/yEw=="

TOKEN="${TELEGRAM_BOT_TOKEN:-}"
if [ -z "$TOKEN" ]; then
  echo "[telegram] TELEGRAM_BOT_TOKEN not set; skipping"
  exit 0
fi
: "${OPENNEKO_PLUGIN_INSTALL_DIR:?need OPENNEKO_PLUGIN_INSTALL_DIR}"
: "${OPENNEKO_PLUGINS_MANIFEST_PATH:?need OPENNEKO_PLUGINS_MANIFEST_PATH}"

echo "[telegram] saving bot token to the secrets store"
openneko secrets set "$PKG" TELEGRAM_BOT_TOKEN "$TOKEN" || echo "[telegram] secrets set returned nonzero"

NM="$OPENNEKO_PLUGIN_INSTALL_DIR/node_modules/$PKG"
if [ ! -f "$NM/dist/run.js" ]; then
  echo "[telegram] installing $PKG@$VER into $OPENNEKO_PLUGIN_INSTALL_DIR"
  ( cd "$OPENNEKO_PLUGIN_INSTALL_DIR" && npm install "$PKG@$VER" --no-audit --no-fund 2>&1 | tail -8 ) \
    || echo "[telegram] npm install returned nonzero"
fi
if [ ! -f "$NM/dist/run.js" ]; then
  echo "[telegram] ERROR: $NM/dist/run.js missing after install; not registering"
  exit 0
fi

echo "[telegram] registering channel manifest entry in $OPENNEKO_PLUGINS_MANIFEST_PATH"
MANIFEST="$OPENNEKO_PLUGINS_MANIFEST_PATH" TG_INTEGRITY="$INTEGRITY" TG_VER="$VER" \
  node --input-type=module <<'NODE'
import { readFileSync, writeFileSync, existsSync } from "node:fs";
const path = process.env.MANIFEST;
const SCHEMA = "https://open-neko.github.io/plugins/manifest.schema.json";
const m = existsSync(path)
  ? JSON.parse(readFileSync(path, "utf8"))
  : { schema: SCHEMA, plugins: [] };
m.schema ||= SCHEMA;
m.plugins ||= [];
const entry = {
  name: "@open-neko/channel-telegram",
  version: process.env.TG_VER,
  integrity: process.env.TG_INTEGRITY,
  permissions: {
    network: ["api.telegram.org"],
    env: [
      { key: "TELEGRAM_BOT_TOKEN", required: true, secret: true, description: "Bot token from @BotFather." },
      { key: "TELEGRAM_WEBHOOK_SECRET", required: false, secret: true, description: "Optional webhook secret_token for verify_inbound." },
    ],
  },
  capabilities: {
    channel: {
      providerLabel: "Telegram",
      directions: ["outbound", "inbound"],
      ingress: "webhook",
      profile: {
        modalities: ["text"],
        richMedia: { markdown: true, cards: false, charts: false, images: true, interactiveControls: true },
        interaction: { turnTaking: "async", canApproveInline: true, quickReplies: true },
        constraints: { maxOutboundChars: 4096, latencyClass: "interactive", attentionModel: "push" },
        fidelity: "summary",
      },
    },
  },
  marketplace: "npm",
};
m.plugins = m.plugins.filter((p) => p && p.name !== entry.name);
m.plugins.push(entry);
writeFileSync(path, JSON.stringify(m, null, 2) + "\n");
console.log(`[telegram] manifest plugins: ${m.plugins.map((p) => p.name).join(", ")}`);
NODE

echo "[telegram] done — worker registers the channel within ~3s"
