package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/masapico/minihub/internal/config"
)

// Run the actual startup path in a subprocess with its own flags and signals.
func TestTransportServerProcess(t *testing.T) {
	path := os.Getenv("MINIHUB_TRANSPORT_TEST_CONFIG")
	if path == "" {
		return
	}
	os.Args = []string{"minihub", "-config", path, "-secure-cookie=false"}
	flag.CommandLine = flag.NewFlagSet("minihub", flag.ContinueOnError)
	if err := run(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func transportCertificate(t *testing.T, dir string) (config.TLS, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	settings := config.TLS{Enabled: true, CertFile: filepath.Join(dir, "server.crt"), KeyFile: filepath.Join(dir, "server.key")}
	if err := os.WriteFile(settings.CertFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid test certificate")
	}
	return settings, roots
}

func transportCommand(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestTransportServerProcess$")
	cmd.Env = append(os.Environ(), "MINIHUB_TRANSPORT_TEST_CONFIG="+path)
	return cmd
}

func writeTransportConfig(t *testing.T, dir string, cfg config.Config) string {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "minihub.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHTTPAndHTTPSStartup(t *testing.T) {
	for _, storage := range []string{"file", "sqlite"} {
		for _, secure := range []bool{false, true} {
			scheme := "http"
			if secure {
				scheme = "https"
			}
			t.Run(storage+"/"+scheme, func(t *testing.T) {
				dir := t.TempDir()
				settings, roots := transportCertificate(t, dir)
				tlsConfig, err := loadServerTLS(settings)
				if err != nil || tlsConfig.MinVersion != tls.VersionTLS12 {
					t.Fatalf("TLS minimum: %v", err)
				}
				settings.Enabled = secure
				reservation, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				addr := reservation.Addr().String()
				if err := reservation.Close(); err != nil {
					t.Fatal(err)
				}
				cfg := config.Defaults()
				cfg.Storage.Type = storage
				cfg.Server.ListenAddress, cfg.Server.DataDir, cfg.Server.TLS = addr, filepath.Join(dir, "data"), settings
				cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile = "server.crt", "server.key"
				cfg.InitialAdmin = &config.InitialAdmin{ID: "admin", Name: "Admin", Password: "transport-test-password"}
				cfg.Notifications.OSNotificationsEnabled = false
				path := writeTransportConfig(t, dir, cfg)
				cmd := transportCommand(t, path)
				var output bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &output
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				finished := false
				t.Cleanup(func() {
					if !finished {
						_ = cmd.Process.Kill()
						<-done
					}
				})
				transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
				defer transport.CloseIdleConnections()
				client := &http.Client{Transport: transport, Timeout: time.Second}
				base := scheme + "://" + addr
				ready := false
				for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
					select {
					case err := <-done:
						finished = true
						t.Fatalf("server exited: %v %s", err, output.String())
					default:
					}
					response, err := client.Get(base + "/login")
					if err == nil {
						response.Body.Close()
						if response.StatusCode == 200 {
							ready = true
							break
						}
					}
					time.Sleep(25 * time.Millisecond)
				}
				if !ready {
					t.Fatal("server did not become ready")
				}
				response, err := client.Post(base+"/api/auth/login", "application/json", strings.NewReader(`{"userId":"admin","password":"transport-test-password"}`))
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || response.StatusCode != 200 {
					t.Fatalf("login: %v %d %s", err, response.StatusCode, body)
				}
				cookies := response.Cookies()
				if len(cookies) != 1 || cookies[0].Secure != secure {
					t.Fatalf("wrong cookies: %+v", cookies)
				}
				request, _ := http.NewRequest(http.MethodGet, base+"/", nil)
				request.AddCookie(cookies[0])
				page, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				pageBody, err := io.ReadAll(page.Body)
				page.Body.Close()
				if err != nil || !bytes.Contains(pageBody, []byte(`data-os-notifications-enabled="false"`)) {
					t.Fatalf("setting not propagated: %v", err)
				}
				dialer := websocket.Dialer{TLSClientConfig: transport.TLSClientConfig, HandshakeTimeout: time.Second}
				conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(base, "http")+"/api/realtime", http.Header{"Cookie": {cookies[0].String()}, "Origin": {base}})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if runtime.GOOS == "windows" {
					// Offline stop must work even after the certificate files disappear.
					if err := os.Remove(settings.CertFile); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(settings.KeyFile); err != nil {
						t.Fatal(err)
					}
					if err := stopServer([]string{"-config", path, "-wait", "15s"}); err != nil {
						t.Fatal(err)
					}
				} else if err := cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					finished = true
					if err != nil {
						t.Fatalf("shutdown: %v %s", err, output.String())
					}
				case <-time.After(15 * time.Second):
					t.Fatal("shutdown timed out")
				}
				conn.SetReadDeadline(time.Now().Add(time.Second))
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
							t.Fatal("shutdown did not close WebSocket")
						}
						break
					}
				}
				log, err := os.ReadFile(filepath.Join(cfg.Server.DataDir, "server.log"))
				if err != nil || !bytes.Contains(log, []byte(`"scheme":"`+scheme+`"`)) {
					t.Fatalf("scheme missing from log: %v", err)
				}
			})
		}
	}
}

func TestInvalidCertificateStopsBeforeStorageInitialization(t *testing.T) {
	dir := t.TempDir()
	settings, _ := transportCertificate(t, dir)
	other, _ := transportCertificate(t, t.TempDir())
	for _, tc := range []struct{ name, cert, key string }{
		{"missing certificate", filepath.Join(dir, "missing"), settings.KeyFile},
		{"missing key", settings.CertFile, filepath.Join(dir, "missing")},
		{"malformed certificate", settings.KeyFile, settings.KeyFile},
		{"malformed key", settings.CertFile, settings.CertFile},
		{"mismatched key", settings.CertFile, other.KeyFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Server.DataDir = filepath.Join(t.TempDir(), "unused-data")
			cfg.Server.TLS = config.TLS{Enabled: true, CertFile: tc.cert, KeyFile: tc.key}
			path := writeTransportConfig(t, t.TempDir(), cfg)
			output, err := transportCommand(t, path).CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte("load server TLS certificate and key")) {
				t.Fatalf("unexpected startup: %v %s", err, output)
			}
			if _, err := os.Stat(cfg.Server.DataDir); !os.IsNotExist(err) {
				t.Fatal("invalid TLS touched storage")
			}
		})
	}
	if cfg, err := loadServerTLS(config.TLS{CertFile: "missing", KeyFile: "missing"}); err != nil || cfg != nil {
		t.Fatal("disabled TLS read certificates")
	}
}
