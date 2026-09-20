// Command auto-sign signs Auto.app without putting its identity in Keychain.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"auto/pkg/signing"
)

const signingTimeout = 2 * time.Minute

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "auto-sign: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) != 2 || (arguments[0] != "sign" && arguments[0] != "verify") {
		return fmt.Errorf("usage: auto-sign <sign|verify> /absolute/path/to/Auto.app")
	}
	context, cancel := context.WithTimeout(context.Background(), signingTimeout)
	defer cancel()
	if arguments[0] == "sign" {
		return signing.SignBundle(context, arguments[1])
	}
	return signing.VerifyBundle(context, arguments[1])
}
