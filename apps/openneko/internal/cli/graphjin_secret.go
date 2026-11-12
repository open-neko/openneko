package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"

	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/gjconfig"
)

// Same lock key as getOrgId() in @neko/db and the AdventureWorks seed.
const orgBootstrapLock = "select pg_advisory_xact_lock(hashtext('openneko.organization.bootstrap'))"

func bootstrapOrg(ctx context.Context, admin db.ConnConfig) (id string, created bool, err error) {
	conn, err := pgx.Connect(ctx, admin.DSN())
	if err != nil {
		return "", false, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, orgBootstrapLock); err != nil {
		return "", false, err
	}
	err = tx.QueryRow(ctx, "select id::text from organization order by created_at asc, id asc limit 1").Scan(&id)
	if err == pgx.ErrNoRows {
		id, err = newUUID()
		if err != nil {
			return "", false, err
		}
		if _, err := tx.Exec(ctx, "insert into organization (id, name) values ($1, $2)", id, "My Workspace"); err != nil {
			return "", false, err
		}
		created = true
	} else if err != nil {
		return "", false, err
	}
	return id, created, tx.Commit(ctx)
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func prepareGraphjinAuth(ctx context.Context, out io.Writer, admin db.ConnConfig, configDir, gjConfig string) error {
	orgID, created, err := bootstrapOrg(ctx, admin)
	if err != nil {
		return fmt.Errorf("find or create the organization: %w", err)
	}
	if created {
		fmt.Fprintf(out, "created organization %s\n", orgID)
	}
	if gjConfig == "" {
		return nil
	}
	secret, err := gjconfig.SigningSecretB64(configDir, orgID)
	if err != nil {
		return err
	}
	r, err := gjconfig.ReconcileFile(gjConfig, secret)
	if err != nil {
		return err
	}
	if r.System {
		fmt.Fprintf(out, "moved the old GraphJin system source to the system block in %s\n", gjConfig)
	}
	if r.Secret {
		fmt.Fprintf(out, "wrote the GraphJin JWT secret for organization %s to %s\n", orgID, gjConfig)
	}
	if r.WritePolicy {
		fmt.Fprintf(out, "turned on GraphJin mutations and listed the worker roles in %s\n", gjConfig)
	}
	return nil
}
