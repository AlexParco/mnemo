package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/AlexParco/mnemo/internal/build"
	"github.com/AlexParco/mnemo/internal/config"
	"github.com/AlexParco/mnemo/internal/httpapi"
	"github.com/spf13/cobra"
)

// `mnemo serve --http` is the server: the process a machine runs so that other
// machines can reach its memory and its mailbox.
//
// It is started by the service rather than by a person, which is why everything
// it needs comes from the config file: a service has none of the user's shell
// environment, so a port or a token in a variable would work when a person ran
// it and fail when the machine did.

// shutdownGrace is how long a request in flight is given when the server is
// asked to stop. A wait can be holding for the best part of a minute, and
// cutting it off would answer a chat with a broken connection rather than with
// the empty answer a timeout is.
const shutdownGrace = 60 * time.Second

func serveOverHTTP(cmd *cobra.Command, resolved config.Settings) error {
	server, err := httpapi.New(httpapi.Options{
		Settings: resolved,
		Store:    memoryStore(resolved),
		Mailbox:  mailboxFor(resolved),
	})
	if err != nil {
		return err
	}

	// stderr, always: a service's output goes to a log file, and a person
	// reading it needs to know which version is running, where it listens and
	// whose files it is answering for.
	complain := cmd.ErrOrStderr()
	fmt.Fprintf(complain, "%s serving on http://%s\n", build.Line(), server.Address())
	fmt.Fprintf(complain, "  store:   %s\n", resolved.Paths.Store)
	fmt.Fprintf(complain, "  mailbox: %s\n", resolved.Paths.Mailbox)

	listening := &http.Server{
		Addr:    server.Address(),
		Handler: server.Handler(),
		// No read or write timeout: a mailbox wait is a request that is meant to
		// take nearly a minute, and a timeout here would end it in a way the
		// client cannot tell from the tunnel dropping.
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Stopping cleanly matters more here than anywhere else: a service is
	// restarted on every update, and a store left mid-write would be the thing
	// an update broke.
	stopping, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	failed := make(chan error, 1)
	go func() {
		err := listening.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		failed <- err
	}()

	select {
	case err := <-failed:
		if err != nil {
			return fmt.Errorf("the server could not listen on %s: %w", server.Address(), err)
		}
		return nil
	case <-stopping.Done():
		fmt.Fprintln(complain, "mnemo: stopping, and waiting for the calls already in flight")
		closing, done := context.WithTimeout(context.WithoutCancel(cmd.Context()), shutdownGrace)
		defer done()
		return listening.Shutdown(closing)
	}
}
