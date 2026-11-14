package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/assets"
	"github.com/open-neko/neko/apps/openneko/internal/jobs"
)

// openNekoSchemas are the schemas that OpenNeko makes outside the
// migrations: the two job queues and the storage contract.
var openNekoSchemas = []string{"pgboss", jobs.DefaultSchema, "openneko_internal"}

type ownedObject struct {
	category string
	name     string
	label    string
}

// ownedObjectsSQL lists what clearOwnedObjectsSQL drops, with the same filters.
const ownedObjectsSQL = `
WITH me AS (SELECT oid FROM pg_roles WHERE rolname = current_user)
SELECT 'schema', n.nspname, 'schema ' || quote_ident(n.nspname)
FROM pg_namespace n, me
WHERE n.nspowner = me.oid AND n.nspname <> 'public'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_namespace'::regclass AND d.objid = n.oid AND d.deptype = 'e')
UNION ALL
SELECT 'relation', c.relname,
  CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view' WHEN 'f' THEN 'foreign table' WHEN 'S' THEN 'sequence' ELSE 'table' END
  || ' public.' || quote_ident(c.relname)
FROM pg_class c, me
WHERE c.relowner = me.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
  AND c.relnamespace = 'public'::regnamespace
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype IN ('e', 'a', 'i'))
UNION ALL
SELECT 'routine', p.proname, CASE p.prokind WHEN 'p' THEN 'procedure ' ELSE 'function ' END || p.oid::regprocedure::text
FROM pg_proc p, me
WHERE p.proowner = me.oid AND p.pronamespace = 'public'::regnamespace AND p.prokind <> 'a'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')
UNION ALL
SELECT 'type', t.typname, CASE t.typtype WHEN 'd' THEN 'domain ' ELSE 'type ' END || t.oid::regtype::text
FROM pg_type t, me
WHERE t.typowner = me.oid AND t.typnamespace = 'public'::regnamespace
  AND t.typtype IN ('e', 'd', 'r', 'm', 'c') AND t.typelem = 0
  AND (t.typtype <> 'c' OR (SELECT relkind FROM pg_class WHERE oid = t.typrelid) = 'c')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e')
ORDER BY 3`

// foreignObjects lists the objects that a restore would drop and that
// OpenNeko did not make: not in a migration, not in an OpenNeko schema, and
// not in the archive's own table of contents.
func foreignObjects(ctx context.Context, conn *pgx.Conn, archived map[string]bool) ([]string, error) {
	known, err := migrationObjectNames()
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, ownedObjectsSQL)
	if err != nil {
		return nil, fmt.Errorf("list the objects that the login owns: %w", err)
	}
	owned, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ownedObject, error) {
		var o ownedObject
		err := row.Scan(&o.category, &o.name, &o.label)
		return o, err
	})
	if err != nil {
		return nil, err
	}
	var foreign []string
	for _, o := range owned {
		key := o.category + ":" + strings.ToLower(o.name)
		if !known[key] && !archived[key] {
			foreign = append(foreign, o.label)
		}
	}
	return foreign, nil
}

var (
	sqlComment      = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)
	sqlName         = `((?:"[^"]+"|[a-z_][a-z0-9_$]*)(?:\s*\.\s*(?:"[^"]+"|[a-z_][a-z0-9_$]*))?)`
	sqlCreateObject = regexp.MustCompile(`\bcreate\s+(?:or\s+replace\s+)?(?:unlogged\s+)?(table|view|materialized\s+view|sequence|foreign\s+table|function|procedure|type|domain|schema)\s+(?:if\s+not\s+exists\s+)?` + sqlName)
	sqlDropObject   = regexp.MustCompile(`\bdrop\s+(table|view|materialized\s+view|sequence|foreign\s+table|function|procedure|routine|type|domain|schema)\s+(?:if\s+exists\s+)?` + sqlName)
	sqlRename       = regexp.MustCompile(`\balter\s+(table|view|materialized\s+view|sequence|foreign\s+table|function|procedure|routine|type|domain|schema)\s+(?:if\s+exists\s+)?(?:only\s+)?` + sqlName + `[^;]*?\brename\s+to\s+` + sqlName)
)

func migrationObjectNames() (map[string]bool, error) {
	known := map[string]bool{"relation:schema_migrations": true}
	for _, schema := range openNekoSchemas {
		known["schema:"+schema] = true
	}
	err := fs.WalkDir(assets.MigrationsFS, "migrations", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		raw, err := fs.ReadFile(assets.MigrationsFS, path)
		if err != nil {
			return err
		}
		text := sqlComment.ReplaceAllString(strings.ToLower(string(raw)), " ")
		for _, m := range sqlCreateObject.FindAllStringSubmatch(text, -1) {
			known[objectKey(m[1], m[2])] = true
		}
		for _, m := range sqlDropObject.FindAllStringSubmatch(text, -1) {
			known[objectKey(m[1], m[2])] = true
		}
		for _, m := range sqlRename.FindAllStringSubmatch(text, -1) {
			known[objectKey(m[1], m[2])] = true
			known[objectKey(m[1], m[3])] = true
		}
		return nil
	})
	return known, err
}

func objectKey(kind, name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 && !strings.Contains(name[i:], `"`) {
		name = name[i+1:]
	}
	name = strings.Trim(strings.TrimSpace(name), `"`)
	return objectCategory(strings.Join(strings.Fields(kind), " ")) + ":" + strings.ToLower(name)
}

func objectCategory(kind string) string {
	switch strings.ToLower(kind) {
	case "function", "procedure", "routine":
		return "routine"
	case "type", "domain":
		return "type"
	case "schema":
		return "schema"
	default:
		return "relation"
	}
}

var archiveEntryKinds = []struct{ prefix, kind string }{
	{"MATERIALIZED VIEW DATA ", ""},
	{"MATERIALIZED VIEW ", "materialized view"},
	{"FOREIGN TABLE ", "foreign table"},
	{"TABLE DATA ", ""},
	{"TABLE ", "table"},
	{"VIEW ", "view"},
	{"SEQUENCE OWNED BY ", ""},
	{"SEQUENCE SET ", ""},
	{"SEQUENCE ", "sequence"},
	{"FUNCTION ", "function"},
	{"PROCEDURE ", "procedure"},
	{"TYPE ", "type"},
	{"DOMAIN ", "domain"},
	{"SCHEMA ", "schema"},
}

// archivedObjectNames reads the object names in a pg_restore --list table of
// contents. An entry reads "id; catalog oid KIND schema name owner".
func archivedObjectNames(contents string) map[string]bool {
	names := map[string]bool{}
	for _, line := range strings.Split(contents, "\n") {
		if strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 || !strings.HasSuffix(fields[0], ";") {
			continue
		}
		rest := strings.Join(fields[3:], " ")
		for _, entry := range archiveEntryKinds {
			if !strings.HasPrefix(rest, entry.prefix) {
				continue
			}
			if entry.kind != "" {
				parts := strings.Fields(strings.TrimPrefix(rest, entry.prefix))
				if len(parts) >= 3 {
					name := strings.Join(parts[1:len(parts)-1], " ")
					if i := strings.Index(name, "("); i >= 0 {
						name = name[:i]
					}
					names[objectCategory(entry.kind)+":"+strings.ToLower(name)] = true
				}
			}
			break
		}
	}
	return names
}

func foreignObjectsError(user, database string, foreign []string) error {
	return fmt.Errorf("%s owns %d objects in database %s that OpenNeko did not make:\n%s\nA restore drops every object that the login owns. OpenNeko needs a database and a login of its own. Nothing was changed",
		user, len(foreign), database, indentList(foreign, len(foreign)))
}

func indentList(items []string, limit int) string {
	shown := items
	if len(shown) > limit {
		shown = shown[:limit]
	}
	text := "  " + strings.Join(shown, "\n  ")
	if len(items) > limit {
		text += fmt.Sprintf("\n  and %d more", len(items)-limit)
	}
	return text
}

func newDatabaseForeignObjectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "foreign-objects",
		Short:  "List the objects that a restore of the pg_dump archive on stdin would drop and that OpenNeko did not make",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := defaultConn()
			if err != nil {
				return err
			}
			foreign, err := listForeignObjects(cmd.Context(), target.DSN(), cmd.InOrStdin())
			if err != nil {
				return err
			}
			for _, item := range foreign {
				fmt.Fprintln(cmd.OutOrStdout(), item)
			}
			return nil
		},
	}
}

func listForeignObjects(ctx context.Context, dsn string, archive io.Reader) ([]string, error) {
	spool, err := spoolArchive(archive)
	if err != nil {
		return nil, err
	}
	defer os.Remove(spool)
	archived, _, err := archiveContents(ctx, spool)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to OpenNeko's database: %w", err)
	}
	defer conn.Close(context.Background())
	return foreignObjects(ctx, conn, archived)
}

func spoolArchive(archive io.Reader) (string, error) {
	spool, err := os.CreateTemp("", "openneko-restore-*.dump")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(spool, bufio.NewReaderSize(archive, 1<<20)); err != nil {
		spool.Close()
		os.Remove(spool.Name())
		return "", fmt.Errorf("read the database archive: %w", err)
	}
	if err := spool.Close(); err != nil {
		os.Remove(spool.Name())
		return "", err
	}
	return spool.Name(), nil
}

func archiveContents(ctx context.Context, spool string) (map[string]bool, string, error) {
	var contents strings.Builder
	if err := runPostgresTool(ctx, nil, &contents, "", "pg_restore", "--list", spool); err != nil {
		return nil, "", err
	}
	return archivedObjectNames(contents.String()), contents.String(), nil
}
