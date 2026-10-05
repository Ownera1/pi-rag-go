package main

import (
	"fmt"
	"github.com/Ownera1/rag-go/internal/command"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, stdout io.Writer) error { return command.Evaluate(args, stdout) }
