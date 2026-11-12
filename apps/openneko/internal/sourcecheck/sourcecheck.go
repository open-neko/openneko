// Package sourcecheck tests one PostgreSQL, MySQL or MariaDB source: the
// host resolves, the port answers, TLS, the login, the rights to read the
// schema, and read-only rights.
package sourcecheck

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Finding struct {
	Step    string   `json:"step"`
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Field   string   `json:"field,omitempty"`
	SQL     []string `json:"sql,omitempty"`
}

type Table struct {
	Name string `json:"name"`
	Rows *int64 `json:"rows"`
}

type Result struct {
	OK         bool      `json:"ok"`
	Supported  bool      `json:"supported"`
	Type       string    `json:"type"`
	Host       string    `json:"host"`
	Port       float64   `json:"port"`
	Database   string    `json:"database"`
	User       string    `json:"user"`
	Findings   []Finding `json:"findings"`
	Tables     []Table   `json:"tables"`
	MoreTables int       `json:"moreTables"`
	Version    string    `json:"serverVersion,omitempty"`
}

type Input struct {
	Type     string
	Host     string
	Port     *float64
	Database string
	User     string
	Password string
	Timeout  time.Duration
}

var Steps = []string{"resolve", "port", "tls", "login", "schema", "read_only"}

const TableLimit = 1000

const PasswordRequired = "OpenNeko needs a login with a password. Save the password for this login first."

var defaultPorts = map[string]float64{"postgres": 5432, "mysql": 3306, "mariadb": 3306}

const placeholderPassword = "<choose a password>"

func dialectOf(kind string) string {
	switch kind {
	case "postgres":
		return "postgres"
	case "mysql", "mariadb":
		return "mysql"
	}
	return ""
}

func pgIdent(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }

func mysqlIdent(v string) string { return "`" + strings.ReplaceAll(v, "`", "``") + "`" }

func mysqlString(v string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "'", "''") + "'"
}

func mysqlAccount(user, host string) string { return mysqlString(user) + "@" + mysqlString(host) }

func ReadOnlyLoginSQL(kind, database, user string) []string {
	if dialectOf(kind) == "mysql" {
		account := mysqlAccount(user, "%")
		return []string{
			fmt.Sprintf("CREATE USER %s IDENTIFIED BY '%s';", account, placeholderPassword),
			fmt.Sprintf("GRANT SELECT, SHOW VIEW ON %s.* TO %s;", mysqlIdent(database), account),
		}
	}
	role := pgIdent(user)
	return []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s';", role, placeholderPassword),
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s;", pgIdent(database), role),
		"GRANT USAGE ON SCHEMA public TO " + role + ";",
		"GRANT SELECT ON ALL TABLES IN SCHEMA public TO " + role + ";",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO " + role + ";",
	}
}

func isIP(host string) bool { return net.ParseIP(host) != nil }

func portText(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) }

func skipRest(findings []Finding, message string) []Finding {
	done := map[string]bool{}
	for _, f := range findings {
		done[f.Step] = true
	}
	for _, step := range Steps {
		if !done[step] {
			findings = append(findings, Finding{Step: step, Status: "skip", Message: message})
		}
	}
	return findings
}

type tlsProbe struct {
	offered, verified          bool
	protocol, reason, serverEr string
}

func readFull(conn net.Conn, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errors.New("the server closed the connection")
		}
		return nil, err
	}
	return buf, nil
}

var protocolNames = map[uint16]string{tls.VersionTLS12: "TLSv1.2", tls.VersionTLS13: "TLSv1.3"}

func handshake(ctx context.Context, conn net.Conn, host string) (tlsProbe, error) {
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	if !isIP(host) {
		cfg.ServerName = host
	}
	secure := tls.Client(conn, cfg)
	if err := secure.HandshakeContext(ctx); err != nil {
		return tlsProbe{}, err
	}
	state := secure.ConnectionState()
	probe := tlsProbe{offered: true, protocol: protocolNames[state.Version]}
	probe.reason = verifyReason(state.PeerCertificates, host)
	probe.verified = probe.reason == ""
	return probe, nil
}

// verifyReason names a failure with the OpenSSL code that Node reports.
func verifyReason(chain []*x509.Certificate, host string) string {
	if len(chain) == 0 {
		return "UNABLE_TO_GET_ISSUER_CERT_LOCALLY"
	}
	opts := x509.VerifyOptions{Intermediates: x509.NewCertPool()}
	for _, c := range chain[1:] {
		opts.Intermediates.AddCert(c)
	}
	_, err := chain[0].Verify(opts)
	if err == nil {
		err = chain[0].VerifyHostname(host)
	}
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostErr x509.HostnameError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &hostErr):
		return "ERR_TLS_CERT_ALTNAME_INVALID"
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired:
		if time.Now().Before(chain[0].NotBefore) {
			return "CERT_NOT_YET_VALID"
		}
		return "CERT_HAS_EXPIRED"
	case errors.As(err, &unknown):
		last := chain[len(chain)-1]
		if len(chain) == 1 && last.CheckSignatureFrom(last) == nil {
			return "DEPTH_ZERO_SELF_SIGNED_CERT"
		}
		if last.CheckSignatureFrom(last) == nil {
			return "SELF_SIGNED_CERT_IN_CHAIN"
		}
		return "UNABLE_TO_VERIFY_LEAF_SIGNATURE"
	}
	return err.Error()
}

func probePostgresTLS(ctx context.Context, address, host string, port float64) (tlsProbe, error) {
	conn, err := dial(ctx, address, port)
	if err != nil {
		return tlsProbe{}, err
	}
	defer conn.Close()
	request := make([]byte, 8)
	binary.BigEndian.PutUint32(request, 8)
	binary.BigEndian.PutUint32(request[4:], 80877103)
	if _, err := conn.Write(request); err != nil {
		return tlsProbe{}, err
	}
	answer, err := readFull(conn, 1)
	if err != nil {
		return tlsProbe{}, err
	}
	switch answer[0] {
	case 'N':
		return tlsProbe{}, nil
	case 'S':
		return handshake(ctx, conn, host)
	}
	return tlsProbe{}, errors.New("unexpected answer to the TLS request")
}

const (
	mysqlClientSSL        = 0x0800
	mysqlClientProtocol41 = 0x0200
)

func probeMysqlTLS(ctx context.Context, address, host string, port float64) (tlsProbe, error) {
	conn, err := dial(ctx, address, port)
	if err != nil {
		return tlsProbe{}, err
	}
	defer conn.Close()
	head, err := readFull(conn, 5)
	if err != nil {
		return tlsProbe{}, err
	}
	if head[4] != 0x0a && head[4] != 0xff {
		return tlsProbe{}, errors.New("unexpected greeting")
	}
	length := int(head[0]) | int(head[1])<<8 | int(head[2])<<16
	if length < 1 {
		return tlsProbe{}, errors.New("unexpected greeting")
	}
	rest, err := readFull(conn, length-1)
	if err != nil {
		return tlsProbe{}, err
	}
	body := append([]byte{head[4]}, rest...)
	if body[0] == 0xff {
		text := ""
		if len(body) > 3 {
			text = string(body[3:])
		}
		return tlsProbe{serverEr: regexp.MustCompile(`^#[0-9A-Z]{5}`).ReplaceAllString(text, "")}, nil
	}
	end := strings.IndexByte(string(body[1:]), 0)
	offset := 1 + end + 1 + 4 + 8 + 1
	if end < 0 || offset+2 > len(body) {
		return tlsProbe{}, errors.New("unexpected greeting")
	}
	capabilities := binary.LittleEndian.Uint16(body[offset:])
	charset := byte(0x21)
	if offset+2 < len(body) {
		charset = body[offset+2]
	}
	if capabilities&mysqlClientSSL == 0 {
		return tlsProbe{}, nil
	}
	request := make([]byte, 4+32)
	request[0] = 32
	request[3] = 1
	binary.LittleEndian.PutUint32(request[4:], mysqlClientSSL|mysqlClientProtocol41)
	binary.LittleEndian.PutUint32(request[8:], 16*1024*1024)
	request[12] = charset
	if _, err := conn.Write(request); err != nil {
		return tlsProbe{}, err
	}
	return handshake(ctx, conn, host)
}

func dial(ctx context.Context, address string, port float64) (net.Conn, error) {
	if port != math.Trunc(port) || port < 0 || port >= 65536 {
		return nil, fmt.Errorf("the port must be a whole number below 65536, not %s", portText(port))
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(address, portText(port)))
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline) //nolint:errcheck
	}
	return conn, nil
}

func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func errorText(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Message
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err.Error()
	}
	return err.Error()
}

var tlsWords = regexp.MustCompile(`(?i)certificate|ssl|tls`)

func postgresLoginFailure(err error, database, user string) Finding {
	f := Finding{Step: "login", Status: "fail"}
	var pgErr *pgconn.PgError
	code := ""
	if errors.As(err, &pgErr) {
		code = pgErr.Code
	}
	switch code {
	case "28P01":
		f.Message = fmt.Sprintf("The database refused the login %s with this password. Save the right password. If the login does not exist yet, ask the database administrator to create it as a read-only login.", user)
		f.Field = "secretRef"
		f.SQL = ReadOnlyLoginSQL("postgres", database, user)
	case "28000":
		f.Message = fmt.Sprintf("The database does not let %s sign in from OpenNeko's server. Ask the database administrator to allow this login from OpenNeko's address in pg_hba.conf. The server said: %s.", user, errorText(err))
		f.Field = "user"
	case "3D000":
		f.Message = fmt.Sprintf("The server has no database named %s. Check the database name.", database)
		f.Field = "dbname"
	case "42501":
		f.Message = fmt.Sprintf("%s may not connect to the database %s.", user, database)
		f.Field = "user"
		f.SQL = []string{fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s;", pgIdent(database), pgIdent(user))}
	default:
		if tlsWords.MatchString(errorText(err)) {
			f.Message = fmt.Sprintf("The TLS connection failed: %s. Ask the database administrator to check the server's TLS setup.", errorText(err))
		} else {
			f.Message = fmt.Sprintf("OpenNeko could not sign in as %s: %s.", user, errorText(err))
		}
	}
	return f
}

func mysqlLoginFailure(err error, kind, database, user string) Finding {
	f := Finding{Step: "login", Status: "fail"}
	var myErr *mysql.MySQLError
	number := uint16(0)
	if errors.As(err, &myErr) {
		number = myErr.Number
	}
	switch number {
	case 1045:
		f.Message = fmt.Sprintf("The database refused the login %s with this password. Save the right password. If the login does not exist yet, ask the database administrator to create it as a read-only login.", user)
		f.Field = "secretRef"
		f.SQL = ReadOnlyLoginSQL(kind, database, user)
	case 1130:
		f.Message = fmt.Sprintf("The database does not let %s sign in from OpenNeko's server. Ask the database administrator to create the login for OpenNeko's address, or for any address ('%%').", user)
		f.Field = "user"
		f.SQL = ReadOnlyLoginSQL(kind, database, user)
	case 1049:
		f.Message = fmt.Sprintf("The server has no database named %s. Check the database name.", database)
		f.Field = "dbname"
	case 1044:
		f.Message = fmt.Sprintf("%s may not read the database %s.", user, database)
		f.Field = "user"
		f.SQL = []string{fmt.Sprintf("GRANT SELECT, SHOW VIEW ON %s.* TO %s;", mysqlIdent(database), mysqlAccount(user, "%"))}
	default:
		f.Message = fmt.Sprintf("OpenNeko could not sign in as %s: %s.", user, errorText(err))
	}
	return f
}

func tableLabel(schema, table string) string {
	if schema == "public" {
		return table
	}
	return schema + "." + table
}

func rowsOf(v sql.NullInt64) *int64 {
	if !v.Valid || v.Int64 < 0 {
		return nil
	}
	return &v.Int64
}

func quoteList(names []string) string {
	const max = 5
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:max], ", "), len(names)-max)
}

func unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

type readCheck struct {
	tables   []Table
	total    int
	findings []Finding
}

func checkPostgresGrants(ctx context.Context, conn *pgx.Conn, database, user string) (readCheck, error) {
	var out readCheck
	rows, err := conn.Query(ctx, `
    select n.nspname as schema, c.relname as name,
           case when c.reltuples < 0 then null else c.reltuples::bigint end as rows,
           has_schema_privilege(n.oid, 'USAGE') and has_table_privilege(c.oid, 'SELECT') as readable,
           c.relkind in ('r', 'p') and has_table_privilege(c.oid, 'INSERT, UPDATE, DELETE, TRUNCATE') as writable,
           pg_has_role(c.relowner, 'USAGE') as owned,
           c.relkind in ('r', 'p') as base
      from pg_class c join pg_namespace n on n.oid = c.relnamespace
     where c.relkind in ('r', 'p', 'v', 'm', 'f')
       and n.nspname not in ('pg_catalog', 'information_schema')
       and n.nspname not like 'pg\_%'
     order by n.nspname = 'public' desc, n.nspname, c.relname`)
	if err != nil {
		return out, err
	}
	type rel struct {
		schema, name                     string
		rows                             sql.NullInt64
		readable, writable, owned, based bool
	}
	var all []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.schema, &r.name, &r.rows, &r.readable, &r.writable, &r.owned, &r.based); err != nil {
			rows.Close()
			return out, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	var readable []rel
	for _, r := range all {
		if r.readable {
			readable = append(readable, r)
		}
	}
	for i, r := range readable {
		if i == TableLimit {
			break
		}
		out.tables = append(out.tables, Table{Name: tableLabel(r.schema, r.name), Rows: rowsOf(r.rows)})
	}
	out.total = len(readable)
	role := pgIdent(user)
	switch {
	case len(all) == 0:
		out.findings = append(out.findings, Finding{Step: "schema", Status: "warn", Message: fmt.Sprintf("The database %s has no tables yet.", database)})
	case len(readable) == 0:
		var schemas []string
		for _, r := range all {
			schemas = append(schemas, r.schema)
		}
		var grants []string
		for _, s := range unique(schemas) {
			grants = append(grants, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s;", pgIdent(s), role),
				fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA %s TO %s;", pgIdent(s), role))
		}
		out.findings = append(out.findings, Finding{Step: "schema", Status: "fail", Field: "user",
			Message: fmt.Sprintf("%s signs in, but cannot read any of the %d tables in %s.", user, len(all), database), SQL: grants})
	default:
		message := fmt.Sprintf("%s can read all %d tables.", user, len(readable))
		if hidden := len(all) - len(readable); hidden > 0 {
			message = fmt.Sprintf("%s can read %d of the %d tables. OpenNeko sees only these %d.", user, len(readable), len(all), len(readable))
		}
		out.findings = append(out.findings, Finding{Step: "schema", Status: "ok", Message: message})
	}

	var super bool
	if err := conn.QueryRow(ctx, "select rolsuper from pg_roles where rolname = current_user").Scan(&super); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var owned, writable, writableSchemas []string
	for _, r := range all {
		if r.based && r.owned {
			owned = append(owned, tableLabel(r.schema, r.name))
		}
		if r.writable && !r.owned {
			writable = append(writable, tableLabel(r.schema, r.name))
			writableSchemas = append(writableSchemas, r.schema)
		}
	}
	switch {
	case super:
		out.findings = append(out.findings, Finding{Step: "read_only", Status: "fail", Field: "user",
			Message: fmt.Sprintf("%s is a superuser, so it can change any data. Use a separate read-only login.", user),
			SQL:     ReadOnlyLoginSQL("postgres", database, user+"_reader")})
	case len(owned) > 0:
		out.findings = append(out.findings, Finding{Step: "read_only", Status: "fail", Field: "user",
			Message: fmt.Sprintf("%s owns %d tables (%s), so it can always change them. Use a separate read-only login.", user, len(owned), quoteList(owned)),
			SQL:     ReadOnlyLoginSQL("postgres", database, user+"_reader")})
	case len(writable) > 0:
		var revokes []string
		for _, s := range unique(writableSchemas) {
			revokes = append(revokes, fmt.Sprintf("REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA %s FROM %s;", pgIdent(s), role))
		}
		out.findings = append(out.findings, Finding{Step: "read_only", Status: "fail", Field: "user",
			Message: fmt.Sprintf("%s can change data in %d tables (%s). OpenNeko needs a read-only login.", user, len(writable), quoteList(writable)),
			SQL:     revokes})
	default:
		out.findings = append(out.findings, Finding{Step: "read_only", Status: "ok", Message: user + " can change no data."})
	}
	return out, nil
}

var (
	grantLine      = regexp.MustCompile(`(?i)^GRANT\s+(.+?)\s+ON\s+`)
	grantOption    = regexp.MustCompile(`(?i)WITH\s+GRANT\s+OPTION`)
	columnList     = regexp.MustCompile(`\s*\(.*\)$`)
	mysqlReadRight = map[string]bool{"SELECT": true, "SHOW VIEW": true, "USAGE": true}
)

func splitOutsideParens(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case r == ',' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func MysqlGrantProblems(grants []string) []string {
	var problems []string
	for _, grant := range grants {
		m := grantLine.FindStringSubmatch(grant)
		if m == nil {
			continue
		}
		if grantOption.MatchString(grant) {
			problems = append(problems, "GRANT OPTION")
		}
		for _, p := range splitOutsideParens(m[1]) {
			p = columnList.ReplaceAllString(strings.ToUpper(strings.TrimSpace(p)), "")
			if p != "" && !mysqlReadRight[p] {
				problems = append(problems, p)
			}
		}
	}
	return unique(problems)
}

func checkMysqlGrants(ctx context.Context, conn *sql.Conn, database, user string) (readCheck, error) {
	var out readCheck
	rows, err := conn.QueryContext(ctx, "SELECT TABLE_NAME AS name, TABLE_ROWS AS row_count FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? ORDER BY TABLE_NAME", database)
	if err != nil {
		return out, err
	}
	count := 0
	for rows.Next() {
		var name string
		var n sql.NullInt64
		if err := rows.Scan(&name, &n); err != nil {
			rows.Close()
			return out, err
		}
		if count < TableLimit {
			out.tables = append(out.tables, Table{Name: name, Rows: rowsOf(n)})
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.total = count
	var account sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT CURRENT_USER() AS account").Scan(&account); err != nil {
		return out, err
	}
	name := user + "@%"
	if account.Valid && strings.Contains(account.String, "@") {
		name = account.String
	}
	at := strings.LastIndex(name, "@")
	quoted := mysqlAccount(name[:at], name[at+1:])
	if count == 0 {
		out.findings = append(out.findings, Finding{Step: "schema", Status: "fail", Field: "user",
			Message: fmt.Sprintf("%s signs in, but sees no tables in %s. Either the database is empty or the login may not read it.", user, database),
			SQL:     []string{fmt.Sprintf("GRANT SELECT, SHOW VIEW ON %s.* TO %s;", mysqlIdent(database), quoted)}})
	} else {
		out.findings = append(out.findings, Finding{Step: "schema", Status: "ok", Message: fmt.Sprintf("%s can read %d tables.", user, count)})
	}
	grantRows, err := conn.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		return out, err
	}
	var grants []string
	for grantRows.Next() {
		var g sql.NullString
		if err := grantRows.Scan(&g); err != nil {
			grantRows.Close()
			return out, err
		}
		grants = append(grants, g.String)
	}
	if err := grantRows.Err(); err != nil {
		return out, err
	}
	if problems := MysqlGrantProblems(grants); len(problems) > 0 {
		out.findings = append(out.findings, Finding{Step: "read_only", Status: "fail", Field: "user",
			Message: fmt.Sprintf("%s holds rights beyond reading (%s). OpenNeko needs a read-only login.", user, quoteList(problems)),
			SQL: []string{
				fmt.Sprintf("REVOKE ALL PRIVILEGES, GRANT OPTION FROM %s;", quoted),
				fmt.Sprintf("GRANT SELECT, SHOW VIEW ON %s.* TO %s;", mysqlIdent(database), quoted),
			}})
	} else {
		out.findings = append(out.findings, Finding{Step: "read_only", Status: "ok", Message: user + " holds read rights only."})
	}
	return out, nil
}

func resolve(ctx context.Context, host string, d time.Duration) (string, error) {
	if isIP(host) {
		return host, nil
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", errors.New("no address")
	}
	return addrs[0].IP.String(), nil
}

// Check runs the steps in order and stops at the first failed step.
func Check(ctx context.Context, in Input) Result {
	kind := strings.ToLower(strings.TrimSpace(in.Type))
	host := strings.TrimSpace(in.Host)
	port := defaultPorts[kind]
	if in.Port != nil {
		port = *in.Port
	}
	d := in.Timeout
	if d <= 0 {
		d = 5 * time.Second
	}
	res := Result{Supported: true, Type: kind, Host: host, Port: port,
		Database: strings.TrimSpace(in.Database), User: strings.TrimSpace(in.User), Tables: []Table{}}
	var findings []Finding
	finish := func() Result {
		findings = skipRest(findings, "Not checked, because an earlier step failed.")
		res.OK = true
		for i := range findings {
			if in.Password != "" {
				findings[i].Message = strings.ReplaceAll(findings[i].Message, in.Password, "[REDACTED]")
			}
			if findings[i].Status == "fail" {
				res.OK = false
			}
		}
		res.Findings = findings
		return res
	}
	dialect := dialectOf(kind)
	if dialect == "" {
		res.Supported = false
		name := kind
		if name == "" {
			name = "this database kind"
		}
		res.Findings = skipRest([]Finding{{Step: "resolve", Status: "skip", Field: "type",
			Message: fmt.Sprintf("OpenNeko has no source check for %s yet. It checks postgres, mysql, mariadb.", name)}}, "Not checked.")
		return res
	}
	var missing []string
	for _, k := range []struct{ key, value string }{{"host", res.Host}, {"database", res.Database}, {"user", res.User}} {
		if k.value == "" {
			missing = append(missing, k.key)
		}
	}
	if len(missing) > 0 {
		field := missing[0]
		if field == "database" {
			field = "dbname"
		}
		words := make([]string, len(missing))
		for i, m := range missing {
			words[i] = m
			if m == "user" {
				words[i] = "login"
			}
		}
		findings = append(findings, Finding{Step: "resolve", Status: "fail", Field: field, Message: fmt.Sprintf("Enter the %s first.", strings.Join(words, ", "))})
		return finish()
	}

	address, err := resolve(ctx, host, d)
	if err != nil {
		findings = append(findings, Finding{Step: "resolve", Status: "fail", Field: "host",
			Message: fmt.Sprintf("OpenNeko's server cannot find the host %s. Check the host name, or enter the IP address.", host)})
		return finish()
	}
	if isIP(host) {
		findings = append(findings, Finding{Step: "resolve", Status: "ok", Message: host + " is an IP address."})
	} else {
		findings = append(findings, Finding{Step: "resolve", Status: "ok", Message: fmt.Sprintf("%s resolves to %s.", host, address)})
	}

	stepCtx, cancel := context.WithTimeout(ctx, d)
	conn, err := dial(stepCtx, address, port)
	cancel()
	if err != nil {
		message := fmt.Sprintf("OpenNeko's server cannot reach %s on port %s: %s.", host, portText(port), errorText(err))
		if errors.Is(err, syscall.ECONNREFUSED) {
			message = fmt.Sprintf("%s refuses connections on port %s. Check the port, and check that the database listens on this address.", host, portText(port))
		} else if timedOut(err) {
			message = fmt.Sprintf("No answer from %s on port %s within %d seconds. A firewall may block OpenNeko's server, or the port is wrong.", host, portText(port), int(math.Round(d.Seconds())))
		}
		findings = append(findings, Finding{Step: "port", Status: "fail", Field: "port", Message: message})
		return finish()
	}
	conn.Close()
	findings = append(findings, Finding{Step: "port", Status: "ok", Message: fmt.Sprintf("Port %s answers.", portText(port))})

	stepCtx, cancel = context.WithTimeout(ctx, d)
	var probe tlsProbe
	if dialect == "postgres" {
		probe, err = probePostgresTLS(stepCtx, address, host, port)
	} else {
		probe, err = probeMysqlTLS(stepCtx, address, host, port)
	}
	cancel()
	if err != nil {
		if timedOut(err) {
			err = errors.New("timed out")
		}
		server := "MySQL or MariaDB"
		if dialect == "postgres" {
			server = "PostgreSQL"
		}
		findings = append(findings, Finding{Step: "tls", Status: "fail", Field: "port",
			Message: fmt.Sprintf("The server does not answer like %s on port %s (%s). Check the database type and the port.", server, portText(port), errorText(err))})
		return finish()
	}
	protocol := probe.protocol
	if protocol == "" {
		protocol = "TLS"
	}
	switch {
	case probe.serverEr != "":
		findings = append(findings, Finding{Step: "tls", Status: "skip", Message: "Not checked, because the server refused the connection before TLS. The login step says why."})
	case !probe.offered:
		findings = append(findings, Finding{Step: "tls", Status: "warn", Message: "TLS is off. The login and the data cross the network unencrypted. Ask the database administrator to turn on TLS."})
	case probe.verified:
		findings = append(findings, Finding{Step: "tls", Status: "ok", Message: fmt.Sprintf("TLS is on (%s) and the certificate is valid for %s.", protocol, host)})
	default:
		findings = append(findings, Finding{Step: "tls", Status: "warn", Message: fmt.Sprintf("TLS is on (%s), but OpenNeko's server cannot verify the certificate (%s). The data is encrypted; the server's identity is not proven.", protocol, probe.reason)})
	}

	if in.Password == "" {
		findings = append(findings, Finding{Step: "login", Status: "fail", Field: "secretRef", Message: PasswordRequired})
		return finish()
	}
	var tlsConfig *tls.Config
	if probe.offered {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
		if !isIP(host) {
			tlsConfig.ServerName = host
		}
	}
	loginCtx, cancel := context.WithTimeout(ctx, 4*d)
	defer cancel()
	var read readCheck
	if dialect == "postgres" {
		cfg, err := pgx.ParseConfig("")
		if err != nil {
			findings = append(findings, postgresLoginFailure(err, res.Database, res.User))
			return finish()
		}
		cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password = address, uint16(port), res.Database, res.User, in.Password
		cfg.TLSConfig, cfg.Fallbacks, cfg.ConnectTimeout = tlsConfig, nil, 2*d
		cfg.RuntimeParams = map[string]string{"application_name": "openneko-source-check",
			"statement_timeout": strconv.FormatInt((2 * d).Milliseconds(), 10)}
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		conn, err := pgx.ConnectConfig(loginCtx, cfg)
		if err != nil {
			findings = append(findings, postgresLoginFailure(err, res.Database, res.User))
			return finish()
		}
		defer conn.Close(context.Background())
		var version string
		if err := conn.QueryRow(loginCtx, "select current_setting('server_version') as version").Scan(&version); err != nil {
			findings = append(findings, schemaFailure(err))
			return finish()
		}
		res.Version = "PostgreSQL " + version
		findings = append(findings, Finding{Step: "login", Status: "ok", Message: fmt.Sprintf("%s signs in to %s.", res.User, res.Version)})
		read, err = checkPostgresGrants(loginCtx, conn, res.Database, res.User)
		if err != nil {
			findings = append(findings, schemaFailure(err))
			return finish()
		}
	} else {
		cfg := mysql.NewConfig()
		cfg.Net, cfg.Addr, cfg.DBName, cfg.User, cfg.Passwd = "tcp", net.JoinHostPort(address, portText(port)), res.Database, res.User, in.Password
		cfg.Timeout, cfg.AllowNativePasswords = 2*d, true
		cfg.TLS = tlsConfig
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			findings = append(findings, mysqlLoginFailure(err, kind, res.Database, res.User))
			return finish()
		}
		db := sql.OpenDB(connector)
		defer db.Close()
		conn, err := db.Conn(loginCtx)
		if err != nil {
			findings = append(findings, mysqlLoginFailure(err, kind, res.Database, res.User))
			return finish()
		}
		defer conn.Close()
		var version sql.NullString
		if err := conn.QueryRowContext(loginCtx, "SELECT VERSION() AS version").Scan(&version); err != nil {
			findings = append(findings, schemaFailure(err))
			return finish()
		}
		text := "unknown"
		if version.Valid {
			text = version.String
		}
		res.Version = "MySQL " + text
		if strings.Contains(strings.ToLower(text), "mariadb") {
			res.Version = "MariaDB " + text
		}
		findings = append(findings, Finding{Step: "login", Status: "ok", Message: fmt.Sprintf("%s signs in to %s.", res.User, res.Version)})
		read, err = checkMysqlGrants(loginCtx, conn, res.Database, res.User)
		if err != nil {
			findings = append(findings, schemaFailure(err))
			return finish()
		}
	}
	findings = append(findings, read.findings...)
	if read.tables != nil {
		res.Tables = read.tables
	}
	res.MoreTables = max(0, read.total-len(read.tables))
	return finish()
}

func schemaFailure(err error) Finding {
	return Finding{Step: "schema", Status: "fail", Message: fmt.Sprintf("OpenNeko signed in, but could not read the schema: %s.", errorText(err))}
}
