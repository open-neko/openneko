package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

var (
	errSourceNotReady    = errors.New("the source check failed; fix the steps marked FAIL, then check again")
	errSourceUnsupported = errors.New("OpenNeko has no source check for this database type yet")
)

type sourceCheckFinding struct {
	Step    string   `json:"step"`
	Status  string   `json:"status"`
	Message string   `json:"message"`
	Field   string   `json:"field,omitempty"`
	SQL     []string `json:"sql,omitempty"`
}

type sourceCheckResult struct {
	OK            bool                 `json:"ok"`
	Supported     bool                 `json:"supported"`
	Type          string               `json:"type"`
	Host          string               `json:"host"`
	Port          int                  `json:"port"`
	Database      string               `json:"database"`
	User          string               `json:"user"`
	ServerVersion string               `json:"serverVersion,omitempty"`
	Findings      []sourceCheckFinding `json:"findings"`
	Tables        []struct {
		Name string `json:"name"`
		Rows *int64 `json:"rows"`
	} `json:"tables"`
	MoreTables int `json:"moreTables"`
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
			body := map[string]any{
				"type":      opts.kind,
				"host":      opts.host,
				"dbname":    opts.database,
				"user":      opts.user,
				"secretRef": opts.secret,
			}
			if opts.port > 0 {
				body["port"] = opts.port
			}
			if opts.passwordStdin {
				password, err := readPasswordLine(cmd.InOrStdin())
				if err != nil {
					return err
				}
				body["password"] = password
			} else if opts.secret == "" {
				return errors.New("give the password with --secret <stored name> or --password-stdin")
			}
			var response struct {
				Check sourceCheckResult `json:"check"`
			}
			raw, err := requestWorkerAPI(cmd.Context(), "source check", http.MethodPost, "/admin/sources/check", body, &response)
			if err != nil {
				return err
			}
			if opts.output == "json" {
				var pretty any
				_ = json.Unmarshal(raw, &pretty)
				encoded, _ := json.MarshalIndent(pretty, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
			} else {
				writeSourceCheck(cmd.OutOrStdout(), response.Check)
			}
			if !response.Check.Supported {
				return errSourceUnsupported
			}
			if !response.Check.OK {
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
	fmt.Fprintf(out, "source: %s %s@%s:%d/%s\n", check.Type, check.User, check.Host, check.Port, check.Database)
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
