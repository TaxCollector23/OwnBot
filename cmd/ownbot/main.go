package main

import (
	"fmt"
	"os"
	"ownbot/internal/cli"
)

func main() {
	if code, handled, err := cli.ReexecActive(os.Args[1:]); handled {
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "ownbot:", err)
		}
		os.Exit(code)
	}
	if err := cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "ownbot:", err)
		os.Exit(1)
	}
}
