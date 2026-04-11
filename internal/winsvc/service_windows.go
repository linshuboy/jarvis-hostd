//go:build windows

package winsvc

import (
	"context"
	"errors"
	"log"

	"golang.org/x/sys/windows/svc"
)

type RunFunc func(context.Context) error

type handler struct {
	baseCtx     context.Context
	serviceName string
	logger      *log.Logger
	run         RunFunc
}

func Run(ctx context.Context, serviceName string, logger *log.Logger, run RunFunc) error {
	if run == nil {
		return errors.New("windows service run function is required")
	}
	return svc.Run(serviceName, &handler{
		baseCtx:     ctx,
		serviceName: serviceName,
		logger:      logger,
		run:         run,
	})
}

func (h *handler) Execute(_ []string, changes <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	runCtx, cancel := context.WithCancel(h.baseCtx)
	defer cancel()
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- h.run(runCtx)
	}()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case <-h.baseCtx.Done():
			status <- svc.Status{State: svc.StopPending}
			cancel()
			<-resultCh
			return false, 0
		case err := <-resultCh:
			if err != nil && !errors.Is(err, context.Canceled) && h.logger != nil {
				h.logger.Printf("windows service %s exited with error: %v", h.serviceName, err)
			}
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		case change := <-changes:
			switch change.Cmd {
			case svc.Interrogate:
				status <- change.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-resultCh
				return false, 0
			default:
			}
		}
	}
}
