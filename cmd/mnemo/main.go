// Command mnemo is a person's project memory, and a mailbox between the agents
// working for them. One static binary, no runtime to install.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/AlexParco/mnemo/internal/cli"
)

func main() {
	// A stopped context is how a long-running command — serve, watch — is asked
	// to finish what it is doing and close cleanly rather than being cut off
	// mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx))
}
