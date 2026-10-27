package main

import (
	"fmt"
	"os"
	"ownbot/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "OwnYourBot:", err)
		os.Exit(1)
	}
}
