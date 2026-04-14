package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"agi/runtime/hostd/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx, os.Args[1:], cli.Dependencies{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}); err != nil && err != context.Canceled {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
