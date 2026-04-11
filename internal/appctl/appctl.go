package appctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	snapshotPath     = "/v1/snapshot"
	setTokenPath     = "/v1/runtime-token"
	clearTokenPath   = "/v1/runtime-token/clear"
	reconnectPath    = "/v1/reconnect"
	httpClientTimout = 3 * time.Second
)

var ErrUnavailable = errors.New("hostd app control is unavailable")

type Snapshot struct {
	RuntimeID         string `json:"runtime_id,omitempty"`
	PairingState      string `json:"pairing_state,omitempty"`
	HasRuntimeToken   bool   `json:"has_runtime_token"`
	LastGatewayURL    string `json:"last_gateway_url,omitempty"`
	LastConnectedAt   string `json:"last_connected_at,omitempty"`
	LastError         string `json:"last_error,omitempty"`
	Online            bool   `json:"online"`
	ConnectionState   string `json:"connection_state,omitempty"`
	HelperPID         int    `json:"helper_pid,omitempty"`
	ControlSocketPath string `json:"control_socket_path,omitempty"`
}

type ServerOptions struct {
	SocketPath        string
	Snapshot          func() (Snapshot, error)
	SetRuntimeToken   func(string) (Snapshot, error)
	ClearRuntimeToken func() (Snapshot, error)
	RequestReconnect  func() (Snapshot, error)
}

type Server struct {
	socketPath string
	listener   net.Listener
	server     *http.Server
}

func Start(ctx context.Context, options ServerOptions) (*Server, error) {
	socketPath := strings.TrimSpace(options.SocketPath)
	if socketPath == "" {
		return nil, fmt.Errorf("app control socket path is required")
	}
	if options.Snapshot == nil {
		return nil, fmt.Errorf("app control snapshot handler is required")
	}
	if options.SetRuntimeToken == nil {
		return nil, fmt.Errorf("app control set-token handler is required")
	}
	if options.ClearRuntimeToken == nil {
		return nil, fmt.Errorf("app control clear-token handler is required")
	}
	if options.RequestReconnect == nil {
		return nil, fmt.Errorf("app control reconnect handler is required")
	}
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
	mux := http.NewServeMux()
	mux.HandleFunc(snapshotPath, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		snapshot, err := options.Snapshot()
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(writer, snapshot)
	})
	mux.HandleFunc(setTokenPath, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var payload struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid request body")
			return
		}
		snapshot, err := options.SetRuntimeToken(payload.Token)
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(writer, snapshot)
	})
	mux.HandleFunc(clearTokenPath, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		snapshot, err := options.ClearRuntimeToken()
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(writer, snapshot)
	})
	mux.HandleFunc(reconnectPath, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		snapshot, err := options.RequestReconnect()
		if err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(writer, snapshot)
	})
	server := &http.Server{
		Handler: mux,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		_ = server.Serve(listener)
	}()
	return &Server{
		socketPath: socketPath,
		listener:   listener,
		server:     server,
	}, nil
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = s.server.Shutdown(shutdownCtx)
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
	return nil
}

func SnapshotStatus(socketPath string) (Snapshot, error) {
	return doJSONRequest[Snapshot](socketPath, http.MethodGet, snapshotPath, nil)
}

func SetRuntimeToken(socketPath string, token string) (Snapshot, error) {
	return doJSONRequest[Snapshot](socketPath, http.MethodPost, setTokenPath, map[string]string{
		"token": strings.TrimSpace(token),
	})
}

func ClearRuntimeToken(socketPath string) (Snapshot, error) {
	return doJSONRequest[Snapshot](socketPath, http.MethodPost, clearTokenPath, map[string]string{})
}

func RequestReconnect(socketPath string) (Snapshot, error) {
	return doJSONRequest[Snapshot](socketPath, http.MethodPost, reconnectPath, map[string]string{})
}

func doJSONRequest[T any](socketPath string, method string, route string, body any) (T, error) {
	var zero T
	client := &http.Client{
		Timeout: httpClientTimout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network string, address string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	defer client.CloseIdleConnections()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			return zero, err
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequest(method, "http://hostd.local"+route, reader)
	if err != nil {
		return zero, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return zero, normalizeUnavailable(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		var payload struct {
			Error string `json:"error"`
		}
		if decodeErr := json.NewDecoder(response.Body).Decode(&payload); decodeErr == nil && strings.TrimSpace(payload.Error) != "" {
			return zero, errors.New(payload.Error)
		}
		return zero, fmt.Errorf("app control request failed with status %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(&zero); err != nil {
		return zero, err
	}
	return zero, nil
}

func normalizeUnavailable(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		if errors.Is(pathError.Err, syscall.ENOENT) || errors.Is(pathError.Err, syscall.ECONNREFUSED) || errors.Is(pathError.Err, syscall.EPERM) || errors.Is(pathError.Err, syscall.EACCES) {
			return ErrUnavailable
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "connect: no such file or directory") {
		return ErrUnavailable
	}
	if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
		return ErrUnavailable
	}
	return err
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, statusCode int, message string) {
	writer.WriteHeader(statusCode)
	writeJSON(writer, map[string]string{"error": message})
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
