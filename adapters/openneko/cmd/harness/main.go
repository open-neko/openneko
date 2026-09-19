package main

import (
	"fmt"
	"github.com/open-neko/harness/adapters/openneko/broker"
	"github.com/open-neko/harness/internal/command"
	"os"
)

func main() {
	lookup, err := broker.GraphJin(os.Getenv("OPENNEKO_BROKER_URL"), os.Getenv("OPENNEKO_BROKER_TOKEN"), os.Getenv("OPENNEKO_DATA_SOURCE_ID"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid GraphJin capability binding")
		os.Exit(2)
	}
	command.Main(lookup)
}
