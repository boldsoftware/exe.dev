// Command sshminisig converts Armored SSH Signatures to the sshminisig format.
//
// Usage:
//
//	ssh-keygen -Y sign -f ~/.ssh/id_ed25519 -n file < message.txt | sshminisig
//
// The output is written to stdout.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/boldsoftware/exe.dev/sshminisig"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "sshminisig: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	minisig, err := sshminisig.Encode(input)
	if err != nil {
		return err
	}

	fmt.Print(minisig)
	return nil
}
