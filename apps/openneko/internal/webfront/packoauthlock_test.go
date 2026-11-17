package webfront

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func TestAFailedPackOAuthUnlockDoesNotKeepTheLock(t *testing.T) {
	db := testpg.Migrated(t)
	a := &API{DB: db}
	saved := packOAuthUnlockSQL
	t.Cleanup(func() { packOAuthUnlockSQL = saved })
	for _, broken := range []string{
		`select pg_advisory_unlock(hashtextextended($1, 1))`,
		`select pg_advisory_unlock(hashtextextended($1::int::text, 0))`,
	} {
		packOAuthUnlockSQL = broken
		release, err := a.lockPackOAuth(t.Context(), "org", "pack", "key")
		if err != nil {
			t.Fatal(err)
		}
		release()
		other, err := pgx.ConnectConfig(t.Context(), db.Config().ConnConfig.Copy())
		if err != nil {
			t.Fatal(err)
		}
		var free bool
		err = other.QueryRow(t.Context(), `select pg_try_advisory_lock(hashtextextended('pack-oauth:org:pack:key', 0))`).Scan(&free)
		_ = other.Close(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !free {
			t.Fatalf("after %q the lock is still held by a pooled connection", broken)
		}
	}
}
