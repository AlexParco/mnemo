// Command fakessh stands in for ssh in the tests of the connected half.
//
// It is here because a tunnel is the one thing those tests cannot do without and
// cannot have: a real ssh needs a host, a key and a network. What a tunnel is,
// from mnemo's side, is a listener on this end that dials the other — so this
// reads the `-L 127.0.0.1:<local>:127.0.0.1:<server>` the keeper passes and does
// exactly that, and nothing else.
//
// It also keeps running until it is told to stop, because that is what the
// keeper watches: ssh exiting is what makes it wait and start again.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	forward := ""
	for i, argument := range os.Args {
		if argument == "-L" && i+1 < len(os.Args) {
			forward = os.Args[i+1]
		}
	}
	if forward == "" {
		fmt.Fprintln(os.Stderr, "fakessh: no -L forward was asked for")
		os.Exit(255)
	}
	// 127.0.0.1:<local>:127.0.0.1:<server>
	parts := strings.Split(forward, ":")
	if len(parts) != 4 {
		fmt.Fprintf(os.Stderr, "fakessh: %q is not a forward this understands\n", forward)
		os.Exit(255)
	}
	local, server := parts[0]+":"+parts[1], parts[2]+":"+parts[3]

	listener, err := net.Listen("tcp", local)
	if err != nil {
		// What a real ssh does when the port is taken, with
		// ExitOnForwardFailure=yes: it gives up rather than running without the
		// forward the caller asked for.
		fmt.Fprintf(os.Stderr, "fakessh: cannot listen on %s: %v\n", local, err)
		os.Exit(255)
	}
	fmt.Fprintf(os.Stderr, "fakessh: forwarding %s to %s\n", local, server)

	go func() {
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			go relay(incoming, server)
		}
	}()

	// Alive until told otherwise, which is what the keeper is watching for.
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGINT, syscall.SIGTERM)
	<-stopping
	listener.Close()
}

func relay(incoming net.Conn, server string) {
	defer incoming.Close()
	outgoing, err := net.Dial("tcp", server)
	if err != nil {
		return
	}
	defer outgoing.Close()
	go io.Copy(outgoing, incoming)
	io.Copy(incoming, outgoing)
}
