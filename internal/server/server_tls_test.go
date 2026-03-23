package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/testutil"
)

// testCA generates a self-signed CA, server cert, and optionally a client cert,
// writing them to the given directory. Returns paths.
type testPKI struct {
	CAFile         string
	ServerCertFile string
	ServerKeyFile  string
	ClientCertFile string
	ClientKeyFile  string
	CACertPool     *x509.CertPool
}

func generateTestPKI(t *testing.T, dir string, withClientCert bool) testPKI {
	t.Helper()

	// Generate CA key and certificate.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)

	caFile := filepath.Join(dir, "ca.pem")
	writePEM(t, caFile, "CERTIFICATE", caDER)

	// Generate server cert signed by CA.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"localhost"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverCertFile := filepath.Join(dir, "server-cert.pem")
	serverKeyFile := filepath.Join(dir, "server-key.pem")
	writePEM(t, serverCertFile, "CERTIFICATE", serverDER)
	writeECKeyPEM(t, serverKeyFile, serverKey)

	pki := testPKI{
		CAFile:         caFile,
		ServerCertFile: serverCertFile,
		ServerKeyFile:  serverKeyFile,
		CACertPool:     caPool,
	}

	if withClientCert {
		clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		clientTemplate := &x509.Certificate{
			SerialNumber: big.NewInt(3),
			Subject:      pkix.Name{CommonName: "test-client"},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		pki.ClientCertFile = filepath.Join(dir, "client-cert.pem")
		pki.ClientKeyFile = filepath.Join(dir, "client-key.pem")
		writePEM(t, pki.ClientCertFile, "CERTIFICATE", clientDER)
		writeECKeyPEM(t, pki.ClientKeyFile, clientKey)
	}

	return pki
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}

func writeECKeyPEM(t *testing.T, path string, key *ecdsa.PrivateKey) {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, path, "EC PRIVATE KEY", der)
}

func newMinimalServer(t *testing.T, yaml string) *Server {
	t.Helper()
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	rt, err := router.New(nil, cfg.Upstreams(), nil)
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	h := proxy.NewHandler(rt, nil, nil, nil)
	return New(cfg, h, rt, nil, nil)
}

func TestServerAcceptsHTTPS(t *testing.T) {
	dir := t.TempDir()
	pki := generateTestPKI(t, dir, false)

	yaml := `
server:
  address: "127.0.0.1:0"
  tls:
    cert_file: ` + pki.ServerCertFile + `
    key_file: ` + pki.ServerKeyFile + `
upstreams:
  - name: dummy
    transport: streamable-http
    url: http://localhost:9999/mcp
`
	srv := newMinimalServer(t, yaml)
	if !srv.TLSEnabled() {
		t.Fatal("expected TLS to be enabled")
	}

	// Start the server on a random port via a listener.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.httpServer.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go srv.httpServer.Serve(ln)
	defer srv.httpServer.Close()

	// Connect with HTTPS client that trusts our CA.
	clientTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: pki.CACertPool,
		},
	}
	testutil.PatchTransport(clientTransport)
	client := &http.Client{Transport: clientTransport}

	resp, err := client.Get("https://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("HTTPS GET failed: %v", err)
	}
	resp.Body.Close()
	// 404 is fine — we didn't register health, just testing TLS handshake.
}

func TestServerRejectsWithoutClientCert(t *testing.T) {
	dir := t.TempDir()
	pki := generateTestPKI(t, dir, true)

	yaml := `
server:
  address: "127.0.0.1:0"
  tls:
    cert_file: ` + pki.ServerCertFile + `
    key_file: ` + pki.ServerKeyFile + `
    client_ca_file: ` + pki.CAFile + `
upstreams:
  - name: dummy
    transport: streamable-http
    url: http://localhost:9999/mcp
`
	srv := newMinimalServer(t, yaml)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.httpServer.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go srv.httpServer.Serve(ln)
	defer srv.httpServer.Close()

	// Client WITHOUT a client cert should fail.
	clientTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: pki.CACertPool,
		},
	}
	testutil.PatchTransport(clientTransport)
	client := &http.Client{Transport: clientTransport}

	_, err = client.Get("https://" + ln.Addr().String() + "/healthz")
	if err == nil {
		t.Fatal("expected error when client cert is missing, got nil")
	}
}

func TestServerAcceptsWithClientCert(t *testing.T) {
	dir := t.TempDir()
	pki := generateTestPKI(t, dir, true)

	yaml := `
server:
  address: "127.0.0.1:0"
  tls:
    cert_file: ` + pki.ServerCertFile + `
    key_file: ` + pki.ServerKeyFile + `
    client_ca_file: ` + pki.CAFile + `
upstreams:
  - name: dummy
    transport: streamable-http
    url: http://localhost:9999/mcp
`
	srv := newMinimalServer(t, yaml)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv.httpServer.TLSConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go srv.httpServer.Serve(ln)
	defer srv.httpServer.Close()

	// Client WITH a valid client cert should succeed.
	clientCert, err := tls.LoadX509KeyPair(pki.ClientCertFile, pki.ClientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	clientTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs:      pki.CACertPool,
			Certificates: []tls.Certificate{clientCert},
		},
	}
	testutil.PatchTransport(clientTransport)
	client := &http.Client{Transport: clientTransport}

	resp, err := client.Get("https://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("HTTPS GET with client cert failed: %v", err)
	}
	resp.Body.Close()
}

func TestNoTLSConfigPlaintext(t *testing.T) {
	yaml := `
server:
  address: "127.0.0.1:0"
upstreams:
  - name: dummy
    transport: streamable-http
    url: http://localhost:9999/mcp
`
	srv := newMinimalServer(t, yaml)
	if srv.TLSEnabled() {
		t.Fatal("expected TLS to be disabled")
	}
}

func TestParseTLSVersion(t *testing.T) {
	tests := []struct {
		input string
		want  uint16
	}{
		{"1.0", tls.VersionTLS10},
		{"1.1", tls.VersionTLS11},
		{"1.2", tls.VersionTLS12},
		{"1.3", tls.VersionTLS13},
		{"", tls.VersionTLS12},
		{"junk", tls.VersionTLS12},
	}
	for _, tt := range tests {
		got := parseTLSVersion(tt.input)
		if got != tt.want {
			t.Errorf("parseTLSVersion(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
