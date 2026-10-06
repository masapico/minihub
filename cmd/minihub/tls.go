package main

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/masapico/minihub/internal/config"
)

// Certificates are read only when starting the server, never by offline commands.
func loadServerTLS(settings config.TLS) (*tls.Config, error) {
	if !settings.Enabled {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(settings.CertFile, settings.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server TLS certificate and key: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}, nil
}

func listenAndServe(server *http.Server) error {
	if server.TLSConfig != nil {
		return server.ListenAndServeTLS("", "")
	}
	return server.ListenAndServe()
}
