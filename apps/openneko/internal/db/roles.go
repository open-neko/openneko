package db

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	FilesRolePrefix         = "neko_files"
	SubscriptionsRolePrefix = "neko_subscriptions"
	maxIdentifierBytes      = 63
)

var (
	plainRoleSuffix = regexp.MustCompile(`^[a-z0-9_]+$`)
	serveRoleName   = regexp.MustCompile(`^neko_(files|subscriptions)(_.*)?$`)
)

// LegacyServeRoles are the cluster-wide roles of releases before the roles
// were named after the database.
var LegacyServeRoles = []string{FilesRolePrefix, SubscriptionsRolePrefix}

func FilesRole(database string) string { return serveRole(FilesRolePrefix, database) }

func SubscriptionsRole(database string) string {
	return serveRole(SubscriptionsRolePrefix, database)
}

// serveRole keeps a plain name that fits. Otherwise it keeps the start of the
// name in [a-z0-9_] and adds eight hex characters of the name's SHA-256, so
// the result stays unique and inside Postgres's 63-byte limit.
func serveRole(prefix, database string) string {
	name := prefix + "_" + database
	if plainRoleSuffix.MatchString(database) && len(name) <= maxIdentifierBytes {
		return name
	}
	sum := sha256.Sum256([]byte(database))
	tag := hex.EncodeToString(sum[:4])
	var b strings.Builder
	for _, r := range strings.ToLower(database) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	kept := b.String()
	if room := maxIdentifierBytes - len(prefix) - 2 - len(tag); len(kept) > room {
		kept = kept[:room]
	}
	return prefix + "_" + kept + "_" + tag
}

// IsServeRole matches the serve roles of any install and any release.
func IsServeRole(name string) bool { return serveRoleName.MatchString(name) }
