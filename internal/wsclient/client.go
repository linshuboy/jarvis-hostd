package wsclient

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/net/websocket"
)

type Conn interface {
	ReadJSON(context.Context, any) error
	WriteJSON(context.Context, any) error
	Close() error
}

type Dialer interface {
	Dial(context.Context, Options) (Conn, error)
}

type Options struct {
	URL            string
	TLSMode        string
	TLSFingerprint string
}

type DefaultDialer struct{}

type jsonConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (DefaultDialer) Dial(ctx context.Context, options Options) (Conn, error) {
	cfg, err := websocket.NewConfig(options.URL, originForURL(options.URL))
	if err != nil {
		return nil, err
	}
	tlsConfig, err := buildTLSConfig(options.TLSMode, options.TLSFingerprint)
	if err != nil {
		return nil, err
	}
	cfg.TlsConfig = tlsConfig
	type result struct {
		conn *websocket.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		connection, dialErr := websocket.DialConfig(cfg)
		done <- result{conn: connection, err: dialErr}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case outcome := <-done:
		if outcome.err != nil {
			return nil, outcome.err
		}
		return &jsonConn{conn: outcome.conn}, nil
	}
}

func (c *jsonConn) ReadJSON(ctx context.Context, value any) error {
	done := make(chan error, 1)
	go func() {
		done <- websocket.JSON.Receive(c.conn, value)
	}()
	select {
	case <-ctx.Done():
		_ = c.conn.Close()
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *jsonConn) WriteJSON(ctx context.Context, value any) error {
	done := make(chan error, 1)
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		done <- websocket.JSON.Send(c.conn, value)
	}()
	select {
	case <-ctx.Done():
		_ = c.conn.Close()
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func (c *jsonConn) Close() error {
	return c.conn.Close()
}

func originForURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "http://localhost"
	}
	scheme := "http"
	if parsed.Scheme == "wss" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, parsed.Host)
}

func buildTLSConfig(mode string, fingerprint string) (*tls.Config, error) {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "system"
	}
	switch mode {
	case "system":
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	case "off":
		return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, nil
	case "pinned":
		normalized := normalizeFingerprint(fingerprint)
		if normalized == "" {
			return nil, fmt.Errorf("tls fingerprint is required when tls_mode=pinned")
		}
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return fmt.Errorf("missing peer certificate")
				}
				actual := sha256.Sum256(state.PeerCertificates[0].Raw)
				if hex.EncodeToString(actual[:]) != normalized {
					return fmt.Errorf("server tls fingerprint mismatch")
				}
				return nil
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported tls mode: %s", mode)
	}
}

func normalizeFingerprint(value string) string {
	raw := strings.ToLower(strings.TrimSpace(value))
	raw = strings.TrimPrefix(raw, "sha256:")
	raw = strings.ReplaceAll(raw, ":", "")
	raw = strings.ReplaceAll(raw, "-", "")
	raw = strings.ReplaceAll(raw, " ", "")
	return raw
}
