//go:build !windows

package appctl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func listenControlSocket(socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, err
	}
	if err := removeSocketIfPresent(socketPath); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if chmodErr := os.Chmod(socketPath, 0o600); chmodErr != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, chmodErr
	}
	return listener, nil
}

func dialControlSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", socketPath)
}

func cleanupControlSocket(socketPath string) error {
	return os.Remove(socketPath)
}

func isUnavailableError(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return errors.Is(pathError.Err, syscall.ENOENT) ||
			errors.Is(pathError.Err, syscall.ECONNREFUSED) ||
			errors.Is(pathError.Err, syscall.EPERM) ||
			errors.Is(pathError.Err, syscall.EACCES)
	}
	return false
}

func removeSocketIfPresent(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("app control path exists and is not a socket: %s", path)
	}
	return os.Remove(path)
}
