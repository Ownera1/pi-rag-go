package main

import (
	"context"
	"fmt"
	"github.com/Ownera1/rag-go/internal/command"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return command.Control(ctx, os.Args[1:], os.Stdout, os.Stderr)
}
