package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/open-neko/neko/apps/openneko/internal/config"
	"github.com/open-neko/neko/apps/openneko/internal/db"
	"github.com/open-neko/neko/apps/openneko/internal/sourcecheck"
)

var (
	errSourceNotReady    = errors.New("the source check failed; fix the steps marked FAIL, then check again")
	errSourceUnsupported = errors.New("OpenNeko has no source check for this database type yet")
)

type sourceCheckFinding = sourcecheck.Finding
type sourceCheckResult = sourcecheck.Result

var checkSource = func(ctx context.Context, opts sourceCheckOptions, password string) (sourceCheckResult, error) {
	in := sourcecheck.Input{
		Type:     strings.TrimSpace(opts.kind),
		Host:     strings.TrimSpace(opts.host),
		Database: strings.TrimSpace(opts.database),
		User:     strings.TrimSpace(opts.user),
		Password: password,
	}
	if in.Type == "" {
		in.Type = "postgres"
	}
	if opts.port > 0 {
		p := float64(opts.port)
		in.Port = &p
	}
	ref := strings.TrimSpace(opts.secret)
	if ref != "" && in.Password == "" {
		stored, found, err := loadStoredSourcePassword(ctx, ref)
		if err != nil {
			return sourceCheckResult{}, err
		}
		if !found {
			check := sourcecheck.Check(ctx, in)
			for i, f := range check.Findings {
				if f.Step == "login" && f.Status == "fail" {
					check.Findings[i].Message = `No stored password is named "` + ref + `". Save the password in the card first.`
				}
			}
			return check, nil
		}
		in.Password = stored
	}
	return sourcecheck.Check(ctx, in), nil
}

func loadStoredSourcePassword(ctx context.Context, name string) (string, bool, error) {
	target, err := defaultConn()
	if err != nil {
		return "", false, err
	}
	conn, err := db.Connect(ctx, target, "openneko source check")
	if err != nil {
		return "", false, err
	}
	defer conn.Close(ctx)
	var enc string
	err = conn.QueryRow(ctx, `select s.value_enc from data_source_secret s
		where s.org_id = (select id from organization order by created_at asc limit 1)
		  and s.name = $1 limit 1`, name).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := config.MaybeDecryptValue("", enc)
	if err != nil {
		return "", false, err
	}
	return plain, true, nil
}

type sourceCheckOptions struct {
	kind, host, database, user, secret, output string
	port                                       int
	passwordStdin                              bool
}

func newSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Check a customer data source that GraphJin reads",
	}
	cmd.AddCommand(newSourceCheckCmd())
	return cmd
}

func newSourceCheckCmd() *cobra.Command {
	var opts sourceCheckOptions
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Test one PostgreSQL, MySQL or MariaDB source: host, port, TLS, login, read rights and read-only rights",
		Long: `Test one database source in order: the host resolves, the port answers,
TLS, the login, the rights to read the schema, and read-only rights.
Each failed step says what is wrong and, where SQL fixes it, prints the
SQL for the database administrator.

Give the password as a stored source secret (--secret, the name saved in
Admin settings) or on standard input (--password-stdin).`,
		Example: `  openneko source check --type postgres --host db.internal --database sales --user reader --secret sales-password
  printf '%s' "$PW" | openneko source check --type mysql --host 10.0.0.5 --database magento --user openneko_ro --password-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.output != "text" && opts.output != "json" {
				return errors.New("--output must be text or json")
			}
			var password string
			if opts.passwordStdin {
				var err error
				password, err = readPasswordLine(cmd.InOrStdin())
				if err != nil {
					return err
				}
			} else if opts.secret == "" {
				return errors.New("give the password with --secret <stored name> or --password-stdin")
			}
			check, err := checkSource(cmd.Context(), opts, password)
			if err != nil {
				return err
			}
			if opts.output == "json" {
				encoded, err := json.MarshalIndent(struct {
					Check sourceCheckResult `json:"check"`
				}{Check: check}, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
			} else {
				writeSourceCheck(cmd.OutOrStdout(), check)
			}
			if !check.Supported {
				return errSourceUnsupported
			}
			if !check.OK {
				return errSourceNotReady
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.kind, "type", "postgres", "Database type: postgres, mysql or mariadb")
	cmd.Flags().StringVar(&opts.host, "host", "", "Host name or IP address of the database")
	cmd.Flags().IntVar(&opts.port, "port", 0, "Port (default 5432 for postgres, 3306 for mysql and mariadb)")
	cmd.Flags().StringVar(&opts.database, "database", "", "Database name")
	cmd.Flags().StringVar(&opts.user, "user", "", "The read-only login")
	cmd.Flags().StringVar(&opts.secret, "secret", "", "Name of the stored source secret that holds the password")
	cmd.Flags().BoolVar(&opts.passwordStdin, "password-stdin", false, "Read the password from standard input")
	cmd.Flags().StringVarP(&opts.output, "output", "o", "text", "Output format: text or json")
	for _, name := range []string{"host", "database", "user"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func readPasswordLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("standard input held no password")
	}
	return password, nil
}

func writeSourceCheck(out io.Writer, check sourceCheckResult) {
	port := strconv.FormatFloat(check.Port, 'f', -1, 64)
	fmt.Fprintf(out, "source: %s %s@%s:%s/%s\n", check.Type, check.User, check.Host, port, check.Database)
	for _, f := range check.Findings {
		level := f.Status
		if level == "fail" {
			level = "FAIL"
		}
		fmt.Fprintf(out, "  %-4s  %-9s %s\n", level, f.Step, f.Message)
		if len(f.SQL) > 0 {
			fmt.Fprintln(out, "        Ask the database administrator to run this SQL:")
			for _, line := range f.SQL {
				fmt.Fprintf(out, "          %s\n", line)
			}
		}
	}
	if !check.OK || len(check.Tables) == 0 {
		return
	}
	fmt.Fprintf(out, "tables the login can read: %d\n", len(check.Tables)+check.MoreTables)
	for _, table := range check.Tables {
		if table.Rows != nil {
			fmt.Fprintf(out, "  %s (about %d rows)\n", table.Name, *table.Rows)
		} else {
			fmt.Fprintf(out, "  %s\n", table.Name)
		}
	}
	if check.MoreTables > 0 {
		fmt.Fprintf(out, "  and %d more\n", check.MoreTables)
	}
}
