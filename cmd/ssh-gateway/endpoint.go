package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"
)

// multiHandler fans slog records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func newMultiHandler(handlers ...slog.Handler) *multiHandler {
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, hh := range h.handlers {
		if hh.Enabled(ctx, r.Level) {
			_ = hh.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		handlers[i] = hh.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, hh := range h.handlers {
		handlers[i] = hh.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}

// endpointWriter is an io.Writer that forwards each write to a tcp or udp
// endpoint asynchronously. Records are dropped (best-effort) if the channel
// buffer is full. Call close() to stop the background goroutine.
type endpointWriter struct {
	u    *url.URL
	ch   chan []byte
	done chan struct{}
}

func newEndpointWriter(endpoint string) (*endpointWriter, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "tcp", "udp", "http", "https":
	default:
		return nil, fmt.Errorf("unsupported log_endpoint scheme %q (want tcp, udp, http, or https)", u.Scheme)
	}
	w := &endpointWriter{
		u:    u,
		ch:   make(chan []byte, 512),
		done: make(chan struct{}),
	}
	switch u.Scheme {
	case "tcp":
		go w.runTCP()
	case "udp":
		go w.runUDP()
	case "http", "https":
		go w.runHTTP()
	}
	return w, nil
}

func (w *endpointWriter) close() {
	close(w.done)
}

func (w *endpointWriter) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	select {
	case w.ch <- b:
	case <-w.done:
	default: // drop if buffer full
	}
	return len(p), nil
}

func (w *endpointWriter) runTCP() {
	var conn net.Conn
	for {
		select {
		case <-w.done:
			if conn != nil {
				conn.Close()
			}
			return
		case b := <-w.ch:
			for {
				if conn == nil {
					var err error
					conn, err = net.DialTimeout("tcp", w.u.Host, 5*time.Second)
					if err != nil {
						select {
						case <-w.done:
							return
						case <-time.After(time.Second):
						}
						continue
					}
				}
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
				if _, err := conn.Write(b); err != nil {
					conn.Close()
					conn = nil
					continue
				}
				break
			}
		}
	}
}

func (w *endpointWriter) runUDP() {
	var conn net.Conn
	for {
		select {
		case <-w.done:
			if conn != nil {
				conn.Close()
			}
			return
		case b := <-w.ch:
			if conn == nil {
				var err error
				conn, err = net.Dial("udp", w.u.Host)
				if err != nil {
					continue
				}
			}
			conn.SetWriteDeadline(time.Now().Add(time.Second)) //nolint:errcheck
			conn.Write(b)                                      //nolint:errcheck
		}
	}
}

func (w *endpointWriter) runHTTP() {
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		select {
		case <-w.done:
			return
		case b := <-w.ch:
			for {
				req, err := http.NewRequest(http.MethodPost, w.u.String(), bytes.NewReader(b))
				if err != nil {
					break // malformed, skip record
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					select {
					case <-w.done:
						return
					case <-time.After(time.Second):
					}
					continue
				}
				resp.Body.Close()
				if resp.StatusCode/100 != 2 {
					select {
					case <-w.done:
						return
					case <-time.After(time.Second):
					}
					continue
				}
				break
			}
		}
	}
}
