package main

import (
	"context"
	"fmt"
	"github.com/Ownera1/rag-go/internal/command"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return command.Prep(ctx, args, stdout, stderr)
}
