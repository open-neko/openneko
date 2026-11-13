package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestJobsRunRefusals(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"jobs", "run", "directory_sync", "--org", "o"}, `can run workflow_cron_sweep; "directory_sync" is not one of them`},
		{[]string{"jobs", "run", "workflow_cron_sweep"}, "--org is required"},
		{[]string{"jobs", "run", "workflow_cron_sweep", "--org", "o", "--at", "tomorrow"}, "--at:"},
	} {
		root := NewRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(c.args)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.args, err)
		}
	}
}
