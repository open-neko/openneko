// Package assets exposes the binary's embedded compose files, migrations, and
// seed scripts. The directory layout matches what the compose supervisor and
// db migrator expect:
//
//	compose/core.yml, dev.yml, demo.yml, bundled-db.yml
//	migrations/*.sql
//	env/.env.example
//	graphjin/customer.sources.yml, a copy of db/graphjin/customer.sources.example.yml
package assets

import "embed"

//go:embed compose/*.yml
var ComposeFS embed.FS

//go:embed migrations/*.sql
var MigrationsFS embed.FS

//go:embed env/.env.example
var DefaultEnvExample []byte

//go:embed graphjin/customer.sources.yml
var GraphJinSeed []byte
