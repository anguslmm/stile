package testutil

import (
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"time"
)

// lingerListener wraps a net.Listener and sets SO_LINGER=0 on all accepted
// connections. This causes Close to send RST instead of FIN, preventing the
// TCP connection from entering TIME_WAIT state. This is critical for test
// suites that create many short-lived servers, as TIME_WAIT accumulation
// can exhaust the macOS ephemeral port range.
type lingerListener struct {
	net.Listener
}

func (l *lingerListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetLinger(0)
	}
	return conn, nil
}

// NewServer creates an httptest.Server with SO_LINGER=0 on all accepted
// connections. Use this instead of httptest.NewServer in tests to prevent
// ephemeral port exhaustion from TIME_WAIT accumulation.
func NewServer(handler http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = &lingerListener{Listener: srv.Listener}
	srv.Start()
	return srv
}

// NewUnstartedServer creates an httptest.Server with SO_LINGER=0 on all
// accepted connections, but does not start it. The caller must call
// srv.Start() or srv.StartTLS() after any additional configuration.
func NewUnstartedServer(handler http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = &lingerListener{Listener: srv.Listener}
	return srv
}

// lingerControl is a net.Dialer Control function that sets SO_LINGER=0 on
// outgoing TCP connections.
func lingerControl(network, address string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		syscall.SetsockoptLinger(int(fd), syscall.SOL_SOCKET, syscall.SO_LINGER,
			&syscall.Linger{Onoff: 1, Linger: 0})
	})
}

// PatchDefaultTransport replaces http.DefaultTransport with a clone that
// disables keep-alive and sets SO_LINGER=0 on all outgoing connections.
// Disabling keep-alive ensures connections are closed immediately after each
// request (no pooling), and SO_LINGER=0 ensures the close sends RST instead
// of FIN, freeing the ephemeral port without entering TIME_WAIT.
//
// Call this in TestMain for packages that use http.DefaultClient, http.Get,
// or http.Post in tests.
func PatchDefaultTransport() {
	dt := http.DefaultTransport.(*http.Transport).Clone()
	dt.DisableKeepAlives = true
	dt.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   lingerControl,
	}).DialContext
	http.DefaultTransport = dt
}

// PatchTransport configures an http.Transport for test use by disabling
// keep-alive and setting SO_LINGER=0 on outgoing connections. Use this
// for HTTP transports that don't use http.DefaultTransport.
func PatchTransport(t *http.Transport) {
	t.DisableKeepAlives = true
	t.DialContext = (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   lingerControl,
	}).DialContext
}
