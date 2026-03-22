package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
)

type testPKI struct {
	CAFile         string
	ServerCertFile string
	ServerKeyFile  string
	ClientCertFile string
	ClientKeyFile  string
	ServerTLS      *tls.Config
}

func generatePKI(t *testing.T, dir string, withClientCert bool) testPKI {
	t.Helper()

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

	caFile := filepath.Join(dir, "ca.pem")
	writePEMFile(t, caFile, "CERTIFICATE", caDER)

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
	writePEMFile(t, serverCertFile, "CERTIFICATE", serverDER)
	writeECKeyFile(t, serverKeyFile, serverKey)

	serverCert, err := tls.LoadX509KeyPair(serverCertFile, serverKeyFile)
	if err != nil {
		t.Fatal(err)
	}

	pki := testPKI{
		CAFile:         caFile,
		ServerCertFile: serverCertFile,
		ServerKeyFile:  serverKeyFile,
		ServerTLS: &tls.Config{
			Certificates: []tls.Certificate{serverCert},
		},
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
		writePEMFile(t, pki.ClientCertFile, "CERTIFICATE", clientDER)
		writeECKeyFile(t, pki.ClientKeyFile, clientKey)

		// Configure the server to require client certs.
		caPool := x509.NewCertPool()
		caPool.AddCert(caCert)
		pki.ServerTLS.ClientCAs = caPool
		pki.ServerTLS.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return pki
}

func writePEMFile(t *testing.T, path, blockType string, der []byte) {
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

func writeECKeyFile(t *testing.T, path string, key *ecdsa.PrivateKey) {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEMFile(t, path, "EC PRIVATE KEY", der)
}

func newUpstreamWithTLS(t *testing.T, url, caFile, certFile, keyFile string, insecure bool) *config.HTTPUpstreamConfig {
	t.Helper()
	yaml := fmt.Sprintf(`
upstreams:
  - name: tls-test
    transport: streamable-http
    url: %s
    tls:
`, url)
	if caFile != "" {
		yaml += fmt.Sprintf("      ca_file: %s\n", caFile)
	}
	if certFile != "" {
		yaml += fmt.Sprintf("      cert_file: %s\n", certFile)
		yaml += fmt.Sprintf("      key_file: %s\n", keyFile)
	}
	if insecure {
		yaml += "      insecure_skip_verify: true\n"
	}
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}
	return cfg.Upstreams()[0].(*config.HTTPUpstreamConfig)
}

func jsonRPCHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := jsonrpc.NewErrorResponse(jsonrpc.IntID(1), jsonrpc.CodeMethodNotFound, "ok")
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

func TestTransportConnectsWithCustomCA(t *testing.T) {
	dir := t.TempDir()
	pki := generatePKI(t, dir, false)

	srv := httptest.NewUnstartedServer(jsonRPCHandler())
	srv.TLS = pki.ServerTLS
	srv.StartTLS()
	defer srv.Close()

	upstream := newUpstreamWithTLS(t, srv.URL, pki.CAFile, "", "", false)
	tr, err := NewHTTPTransport(upstream)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test",
		ID:      jsonrpc.IntID(1),
	}

	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip with custom CA failed: %v", err)
	}
	jr := result.(*JSONResult)
	if jr.Response().Error.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("unexpected response code: %d", jr.Response().Error.Code)
	}
}

func TestTransportSendsClientCertForMTLS(t *testing.T) {
	dir := t.TempDir()
	pki := generatePKI(t, dir, true)

	srv := httptest.NewUnstartedServer(jsonRPCHandler())
	srv.TLS = pki.ServerTLS
	srv.StartTLS()
	defer srv.Close()

	upstream := newUpstreamWithTLS(t, srv.URL, pki.CAFile, pki.ClientCertFile, pki.ClientKeyFile, false)
	tr, err := NewHTTPTransport(upstream)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test",
		ID:      jsonrpc.IntID(1),
	}

	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip with mTLS failed: %v", err)
	}
	jr := result.(*JSONResult)
	if jr.Response().Error.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("unexpected response code: %d", jr.Response().Error.Code)
	}
}

func TestTransportFailsWithoutClientCertWhenRequired(t *testing.T) {
	dir := t.TempDir()
	pki := generatePKI(t, dir, true)

	srv := httptest.NewUnstartedServer(jsonRPCHandler())
	srv.TLS = pki.ServerTLS
	srv.StartTLS()
	defer srv.Close()

	// Connect with CA but WITHOUT client cert — should fail.
	upstream := newUpstreamWithTLS(t, srv.URL, pki.CAFile, "", "", false)
	tr, err := NewHTTPTransport(upstream)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test",
		ID:      jsonrpc.IntID(1),
	}

	_, err = tr.RoundTrip(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when client cert is required but not provided")
	}
}

func TestTransportInsecureSkipVerify(t *testing.T) {
	dir := t.TempDir()
	pki := generatePKI(t, dir, false)

	srv := httptest.NewUnstartedServer(jsonRPCHandler())
	srv.TLS = pki.ServerTLS
	srv.StartTLS()
	defer srv.Close()

	// Connect without CA file but with insecure_skip_verify — should succeed.
	upstream := newUpstreamWithTLS(t, srv.URL, "", "", "", true)
	tr, err := NewHTTPTransport(upstream)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test",
		ID:      jsonrpc.IntID(1),
	}

	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip with insecure_skip_verify failed: %v", err)
	}
	jr := result.(*JSONResult)
	if jr.Response().Error.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("unexpected response code: %d", jr.Response().Error.Code)
	}
}

func TestTransportFailsWithBadCAFile(t *testing.T) {
	dir := t.TempDir()
	badCA := filepath.Join(dir, "bad-ca.pem")
	os.WriteFile(badCA, []byte("not a cert"), 0o644)

	yaml := fmt.Sprintf(`
upstreams:
  - name: bad-ca
    transport: streamable-http
    url: https://example.com
    tls:
      ca_file: %s
`, badCA)
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	h := cfg.Upstreams()[0].(*config.HTTPUpstreamConfig)
	_, err = NewHTTPTransport(h)
	if err == nil {
		t.Fatal("expected error for bad CA file")
	}
}

func TestTransportFailsWithMissingCAFile(t *testing.T) {
	yaml := `
upstreams:
  - name: missing-ca
    transport: streamable-http
    url: https://example.com
    tls:
      ca_file: /nonexistent/ca.pem
`
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	h := cfg.Upstreams()[0].(*config.HTTPUpstreamConfig)
	_, err = NewHTTPTransport(h)
	if err == nil {
		t.Fatal("expected error for missing CA file")
	}
}
