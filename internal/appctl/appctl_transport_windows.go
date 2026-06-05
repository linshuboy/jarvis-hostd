//go:build windows

package appctl

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
)

func listenControlSocket(socketPath string) (net.Listener, error) {
	return winio.ListenPipe(socketPath, nil)
}

func dialControlSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, socketPath)
}

func cleanupControlSocket(_ string) error {
	return nil
}

func isUnavailableError(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "the system cannot find the file specified") ||
		strings.Contains(message, "no process is on the other end of the pipe") ||
		strings.Contains(message, "actively refused") ||
		strings.Contains(message, "all pipe instances are busy") ||
		strings.Contains(message, "access is denied")
}
