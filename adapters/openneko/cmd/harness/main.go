package main

import (
	"fmt"
	"github.com/open-neko/harness/adapters/openneko/broker"
	"github.com/open-neko/harness/internal/agent"
	"github.com/open-neko/harness/internal/command"
	"os"
)

func main() {
	lookup, err := broker.GraphJin(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"), os.Getenv("OPENNEKO_DATA_SOURCE_ID"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid GraphJin capability binding")
		os.Exit(2)
	}
	tools := agent.Tools{Lookup: lookup}
	// Only the trusted launcher enables this capability after installing its broker profile.
	if os.Getenv("OPENNEKO_HARNESS_PROPOSALS") == "1" {
		tools.Propose, err = broker.Propose(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid proposal capability binding")
			os.Exit(2)
		}
	}
	command.MainWithTools(tools)
}
