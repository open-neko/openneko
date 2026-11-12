package sourcecheck

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/open-neko/neko/apps/openneko/internal/testpg"
)

func finding(t *testing.T, r Result, step string) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Step == step {
			return f
		}
	}
	t.Fatalf("no %s finding in %+v", step, r.Findings)
	return Finding{}
}

func statuses(r Result) string {
	var s []string
	for _, f := range r.Findings {
		s = append(s, f.Step+"="+f.Status)
	}
	return strings.Join(s, " ")
}

func port(p int) *float64 { f := float64(p); return &f }

func fakeServer(t *testing.T, serve func(net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); serve(c) }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	cfg := srv.TLS.Clone()
	srv.Close()
	return cfg
}

func TestUnsupportedAndMissing(t *testing.T) {
	r := Check(context.Background(), Input{Type: "Oracle", Host: "h", Database: "d", User: "u"})
	if r.Supported || r.OK || r.Type != "oracle" || r.Port != 0 || len(r.Findings) != 6 {
		t.Fatalf("%+v", r)
	}
	if f := r.Findings[0]; f.Field != "type" || f.Message != "OpenNeko has no source check for oracle yet. It checks postgres, mysql, mariadb." {
		t.Fatalf("%+v", f)
	}
	if f := r.Findings[1]; f.Status != "skip" || f.Message != "Not checked." {
		t.Fatalf("%+v", f)
	}
	r = Check(context.Background(), Input{Type: "mysql", Host: " ", User: "u"})
	if r.Port != 3306 || statuses(r) != "resolve=fail port=skip tls=skip login=skip schema=skip read_only=skip" {
		t.Fatalf("%+v", r)
	}
	if f := r.Findings[0]; f.Field != "host" || f.Message != "Enter the host, database first." {
		t.Fatalf("%+v", f)
	}
	r = Check(context.Background(), Input{Type: "postgres", Host: "h"})
	if f := r.Findings[0]; f.Field != "dbname" || f.Message != "Enter the database, login first." {
		t.Fatalf("%+v", f)
	}
}

func TestResolveAndPort(t *testing.T) {
	r := Check(context.Background(), Input{Type: "postgres", Host: "no-such-host.invalid", Database: "d", User: "u"})
	if f := finding(t, r, "resolve"); f.Status != "fail" || f.Field != "host" {
		t.Fatalf("%+v", f)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	r = Check(context.Background(), Input{Type: "postgres", Host: "127.0.0.1", Port: port(closed), Database: "d", User: "u"})
	if f := finding(t, r, "resolve"); f.Message != "127.0.0.1 is an IP address." {
		t.Fatalf("%+v", f)
	}
	want := fmt.Sprintf("127.0.0.1 refuses connections on port %d. Check the port, and check that the database listens on this address.", closed)
	if f := finding(t, r, "port"); f.Status != "fail" || f.Field != "port" || f.Message != want {
		t.Fatalf("%+v", f)
	}
	r = Check(context.Background(), Input{Type: "postgres", Host: "localhost", Port: port(closed), Database: "d", User: "u"})
	if f := finding(t, r, "resolve"); f.Status != "ok" || !strings.HasPrefix(f.Message, "localhost resolves to ") {
		t.Fatalf("%+v", f)
	}
	r = Check(context.Background(), Input{Type: "postgres", Host: "127.0.0.1", Port: port(70000), Database: "d", User: "u"})
	if f := finding(t, r, "port"); f.Status != "fail" {
		t.Fatalf("%+v", f)
	}
}

func TestTLSProbes(t *testing.T) {
	cert := selfSignedTLS(t)
	pgTLS := fakeServer(t, func(c net.Conn) {
		buf := make([]byte, 8)
		if _, err := c.Read(buf); err != nil {
			return
		}
		c.Write([]byte("S"))
		tls.Server(c, cert).Handshake()
	})
	pgPlain := fakeServer(t, func(c net.Conn) {
		buf := make([]byte, 8)
		if _, err := c.Read(buf); err != nil {
			return
		}
		c.Write([]byte("N"))
	})
	pgWrong := fakeServer(t, func(c net.Conn) {
		buf := make([]byte, 8)
		c.Read(buf)
		c.Write([]byte("HTTP/1.1 400"))
	})
	silent := fakeServer(t, func(c net.Conn) { time.Sleep(3 * time.Second) })
	greeting := func(caps uint16) []byte {
		body := []byte{0x0a}
		body = append(body, "8.4.13\x00"...)
		body = append(body, 1, 0, 0, 0)
		body = append(body, "abcdefgh"...)
		body = append(body, 0)
		body = binary.LittleEndian.AppendUint16(body, caps)
		body = append(body, 0xff, 2, 0)
		return append([]byte{byte(len(body)), 0, 0, 0}, body...)
	}
	myTLS := fakeServer(t, func(c net.Conn) {
		c.Write(greeting(0xffff))
		buf := make([]byte, 36)
		if _, err := c.Read(buf); err != nil || buf[12] != 0xff {
			return
		}
		tls.Server(c, cert).Handshake()
	})
	myPlain := fakeServer(t, func(c net.Conn) { c.Write(greeting(0xf7ff)) })
	myRefused := fakeServer(t, func(c net.Conn) {
		body := append([]byte{0xff, 0x6a, 0x04}, "Host 'x' is not allowed to connect"...)
		c.Write(append([]byte{byte(len(body)), 0, 0, 0}, body...))
	})
	for _, c := range []struct {
		kind, status, message string
		port                  int
	}{
		{"postgres", "warn", "TLS is on (TLSv1.3), but OpenNeko's server cannot verify the certificate (", pgTLS},
		{"postgres", "warn", "TLS is off. The login", pgPlain},
		{"postgres", "fail", "The server does not answer like PostgreSQL on port " + strconv.Itoa(pgWrong) + " (unexpected answer to the TLS request).", pgWrong},
		{"postgres", "fail", "The server does not answer like PostgreSQL on port " + strconv.Itoa(silent) + " (timed out).", silent},
		{"mariadb", "warn", "TLS is on (TLSv1.3), but OpenNeko's server cannot verify the certificate (", myTLS},
		{"mysql", "warn", "TLS is off.", myPlain},
		{"mysql", "skip", "Not checked, because the server refused the connection before TLS.", myRefused},
	} {
		r := Check(context.Background(), Input{Type: c.kind, Host: "127.0.0.1", Port: port(c.port), Database: "d", User: "u", Timeout: time.Second})
		f := finding(t, r, "tls")
		if f.Status != c.status || !strings.HasPrefix(f.Message, c.message) {
			t.Errorf("%s %d: %+v", c.kind, c.port, f)
		}
		if c.status != "fail" {
			if l := finding(t, r, "login"); l.Status != "fail" || l.Field != "secretRef" || l.Message != PasswordRequired {
				t.Errorf("%s login: %+v", c.kind, l)
			}
		}
	}
}

func TestMysqlGrantProblems(t *testing.T) {
	got := MysqlGrantProblems([]string{
		"GRANT USAGE ON *.* TO `ro`@`%`",
		"GRANT SELECT, SHOW VIEW ON `shop`.* TO `ro`@`%`",
		"GRANT SELECT (a, b), INSERT, update ON `shop`.`t` TO `ro`@`%` WITH GRANT OPTION",
		"GRANT `role1` TO `ro`@`%`",
		"REVOKE x",
	})
	if !reflect.DeepEqual(got, []string{"GRANT OPTION", "INSERT", "UPDATE"}) {
		t.Fatalf("%q", got)
	}
	if MysqlGrantProblems(nil) != nil {
		t.Fatal("nil")
	}
	if q := quoteList([]string{"a", "b", "c", "d", "e", "f", "g"}); q != "a, b, c, d, e and 2 more" {
		t.Fatal(q)
	}
}

func pgTarget(t *testing.T) (*pgx.Conn, *url.URL) {
	t.Helper()
	raw := testpg.URL(t)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn, u
}

func pgExec(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestPostgres(t *testing.T) {
	admin, u := pgTarget(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	db := "sc_" + suffix
	reader, writer, owner := "sc_reader_"+suffix, "sc_writer_"+suffix, "sc_owner_"+suffix
	pgExec(t, admin, "create database "+db)
	t.Cleanup(func() {
		admin.Exec(context.Background(), "drop database if exists "+db+" with (force)")
		for _, r := range []string{reader, writer, owner} {
			admin.Exec(context.Background(), "drop role if exists "+r)
		}
	})
	for _, r := range []string{reader, writer, owner} {
		pgExec(t, admin, fmt.Sprintf("create role %s login password 'pw-%s'", r, r))
	}
	target := *u
	target.Path = "/" + db
	dbConn, err := pgx.Connect(context.Background(), target.String())
	if err != nil {
		t.Fatal(err)
	}
	defer dbConn.Close(context.Background())
	host, p, _ := net.SplitHostPort(u.Host)
	pn, _ := strconv.Atoi(p)
	superUser := u.User.Username()
	superPW, _ := u.User.Password()
	check := func(user, password, database string) Result {
		return Check(context.Background(), Input{Type: "postgres", Host: host, Port: port(pn), Database: database, User: user, Password: password})
	}

	r := check(reader, "pw-"+reader, db)
	if f := finding(t, r, "schema"); f.Status != "warn" || f.Message != "The database "+db+" has no tables yet." {
		t.Fatalf("%+v", f)
	}
	if !r.OK || !strings.HasPrefix(r.Version, "PostgreSQL ") || finding(t, r, "login").Message != reader+" signs in to "+r.Version+"." {
		t.Fatalf("%+v", r)
	}

	pgExec(t, dbConn, "create schema sales; create table public.orders (id int); create table sales.lines (id int); create view public.v as select 1 as x")
	pgExec(t, dbConn, "grant usage on schema sales to "+writer+"; alter table public.orders owner to "+owner)
	r = check(reader, "pw-"+reader, db)
	f := finding(t, r, "schema")
	if f.Status != "fail" || f.Message != fmt.Sprintf("%s signs in, but cannot read any of the 3 tables in %s.", reader, db) ||
		!reflect.DeepEqual(f.SQL, []string{
			`GRANT USAGE ON SCHEMA "public" TO "` + reader + `";`,
			`GRANT SELECT ON ALL TABLES IN SCHEMA "public" TO "` + reader + `";`,
			`GRANT USAGE ON SCHEMA "sales" TO "` + reader + `";`,
			`GRANT SELECT ON ALL TABLES IN SCHEMA "sales" TO "` + reader + `";`,
		}) {
		t.Fatalf("%+v", f)
	}

	pgExec(t, dbConn, "grant select on public.orders, public.v to "+reader)
	r = check(reader, "pw-"+reader, db)
	if f := finding(t, r, "schema"); f.Status != "ok" || f.Message != reader+" can read 2 of the 3 tables. OpenNeko sees only these 2." {
		t.Fatalf("%+v", f)
	}
	if f := finding(t, r, "read_only"); f.Status != "ok" || f.Message != reader+" can change no data." {
		t.Fatalf("%+v", f)
	}
	if len(r.Tables) != 2 || r.Tables[0].Name != "orders" || r.Tables[1].Name != "v" || r.MoreTables != 0 || !r.OK {
		t.Fatalf("%+v", r)
	}

	pgExec(t, dbConn, "grant select, insert on all tables in schema public, sales to "+writer)
	r = check(writer, "pw-"+writer, db)
	f = finding(t, r, "read_only")
	if f.Status != "fail" || f.Message != writer+" can change data in 2 tables (orders, sales.lines). OpenNeko needs a read-only login." ||
		!reflect.DeepEqual(f.SQL, []string{
			`REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA "public" FROM "` + writer + `";`,
			`REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON ALL TABLES IN SCHEMA "sales" FROM "` + writer + `";`,
		}) || r.OK || len(r.Tables) != 3 || r.Tables[2].Name != "sales.lines" {
		t.Fatalf("%+v", r)
	}

	r = check(owner, "pw-"+owner, db)
	if f := finding(t, r, "read_only"); f.Status != "fail" || f.Message != owner+" owns 1 tables (orders), so it can always change them. Use a separate read-only login." || f.SQL[0] != `CREATE ROLE "`+owner+`_reader" LOGIN PASSWORD '<choose a password>';` {
		t.Fatalf("%+v", f)
	}

	r = check(superUser, superPW, db)
	if f := finding(t, r, "read_only"); f.Status != "fail" || f.Message != superUser+" is a superuser, so it can change any data. Use a separate read-only login." {
		t.Fatalf("%+v", f)
	}

	r = check(reader, "wrong-pw", db)
	f = finding(t, r, "login")
	if f.Status != "fail" || f.Field != "secretRef" || len(f.SQL) != 5 || statuses(r) != "resolve=ok port=ok tls=warn login=fail schema=skip read_only=skip" {
		t.Fatalf("%+v", r)
	}

	r = check(reader, "pw-"+reader, db+"_missing")
	if f := finding(t, r, "login"); f.Field != "dbname" || f.Message != "The server has no database named "+db+"_missing. Check the database name." {
		t.Fatalf("%+v", f)
	}

	pgExec(t, admin, "revoke connect on database "+db+" from public")
	r = check(reader, "pw-"+reader, db)
	if f := finding(t, r, "login"); f.Field != "user" || f.SQL[0] != `GRANT CONNECT ON DATABASE "`+db+`" TO "`+reader+`";` {
		t.Fatalf("%+v", f)
	}

	planted := "planted-" + suffix
	pgExec(t, admin, fmt.Sprintf("create role %q login password '%s'", planted, planted))
	t.Cleanup(func() { admin.Exec(context.Background(), fmt.Sprintf("drop role if exists %q", planted)) })
	r = check(planted, planted, db)
	for _, f := range r.Findings {
		if strings.Contains(f.Message, planted) {
			t.Fatalf("password in %+v", f)
		}
	}
	if f := finding(t, r, "login"); !strings.Contains(f.Message, "[REDACTED]") {
		t.Fatalf("%+v", f)
	}
}

func TestMysql(t *testing.T) {
	dsn := os.Getenv("OPENNEKO_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set OPENNEKO_TEST_MYSQL_DSN to a MySQL root DSN, for example root:pw@tcp(127.0.0.1:3306)/")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	connector, _ := mysql.NewConnector(cfg)
	root := sql.OpenDB(connector)
	defer root.Close()
	suffix := strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	db, ro, rw := "sc_"+suffix, "sc_ro_"+suffix, "sc_rw_"+suffix
	for _, q := range []string{
		"CREATE DATABASE " + db,
		"CREATE TABLE " + db + ".orders (id int)",
		"CREATE TABLE " + db + ".lines (id int)",
		"CREATE USER '" + ro + "'@'%' IDENTIFIED BY 'pw'",
		"CREATE USER '" + rw + "'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT, SHOW VIEW ON " + db + ".* TO '" + ro + "'@'%'",
		"GRANT SELECT, INSERT ON " + db + ".* TO '" + rw + "'@'%' WITH GRANT OPTION",
	} {
		if _, err := root.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		root.Exec("DROP DATABASE IF EXISTS " + db)
		root.Exec("DROP USER IF EXISTS '" + ro + "'@'%', '" + rw + "'@'%'")
	})
	host, p, _ := net.SplitHostPort(cfg.Addr)
	pn, _ := strconv.Atoi(p)
	check := func(user, password, database string) Result {
		return Check(context.Background(), Input{Type: "mysql", Host: host, Port: port(pn), Database: database, User: user, Password: password})
	}
	r := check(ro, "pw", db)
	if !r.OK || !strings.HasPrefix(r.Version, "MySQL 8") || len(r.Tables) != 2 || r.Tables[0].Name != "lines" ||
		finding(t, r, "schema").Message != ro+" can read 2 tables." || finding(t, r, "read_only").Message != ro+" holds read rights only." {
		t.Fatalf("%+v", r)
	}
	if f := finding(t, r, "tls"); f.Status != "warn" || !strings.HasPrefix(f.Message, "TLS is on (TLSv1.3), but") {
		t.Fatalf("%+v", f)
	}
	r = check(rw, "pw", db)
	f := finding(t, r, "read_only")
	if f.Status != "fail" || f.Message != rw+" holds rights beyond reading (GRANT OPTION, INSERT). OpenNeko needs a read-only login." ||
		f.SQL[0] != "REVOKE ALL PRIVILEGES, GRANT OPTION FROM '"+rw+"'@'%';" {
		t.Fatalf("%+v", f)
	}
	r = check(ro, "wrong", db)
	if f := finding(t, r, "login"); f.Field != "secretRef" || f.SQL[0] != "CREATE USER '"+ro+"'@'%' IDENTIFIED BY '<choose a password>';" {
		t.Fatalf("%+v", f)
	}
	r = check(ro, "pw", db+"_x")
	if f := finding(t, r, "login"); f.Field != "user" || f.Message != ro+" may not read the database "+db+"_x." {
		t.Fatalf("%+v", f)
	}
}
