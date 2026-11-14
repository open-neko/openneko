# `@neko/web`

The OpenNeko web application. Every page is a client-rendered route in the
static single-page app (`serverPages` in `src/spa/server-pages.ts` is empty).

`pnpm --filter @neko/web build:spa` builds the static bundle into
`apps/web/dist`, including `/manifest.webmanifest` and `/sw.js` (with the
build's shell version stamped in `scripts/stamp-sw.mjs`) for the installable
mobile web app and Web Push notifications. `openneko web` serves `apps/web/dist`
and answers the `/api/v1/*` routes in Go.

## Commands

```bash
pnpm --filter @neko/web build:spa     # build the static SPA into apps/web/dist
pnpm --filter @neko/web typecheck     # TypeScript check
pnpm --filter @neko/web test          # Vitest unit and contract alignment tests
pnpm --filter @neko/web test:phone    # Playwright 390x844 phone suite against openneko web
```

See [INSTALL.md](../../INSTALL.md) and [CONTRIBUTING.md](../../CONTRIBUTING.md)
for the full developer setup.

