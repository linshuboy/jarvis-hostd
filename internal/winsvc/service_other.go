//go:build !windows

package winsvc

import (
	"context"
	"fmt"
	"log"
)

type RunFunc func(context.Context) error

func Run(ctx context.Context, serviceName string, logger *log.Logger, run RunFunc) error {
	_ = ctx
	_ = serviceName
	_ = logger
	_ = run
	return fmt.Errorf("windows service mode is only supported on windows")
}
