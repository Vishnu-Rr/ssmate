package awsconf

import (
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	sharedOnce   sync.Once
	sharedClient *http.Client
)

// SharedHTTPClient returns one HTTP client for every AWS call this process
// makes.
//
// This matters far more than it looks. The SDK builds a fresh client, and so
// a fresh connection pool, for every aws.Config. Sweeping twenty regions with
// a config each therefore performs forty-odd separate TLS handshakes and
// reuses nothing.
//
// On a fast network that is merely wasteful. Behind a TLS-inspecting corporate
// proxy, where a handshake can take several seconds, it is the entire cost of
// the sweep: measured at 3.7s of handshake against 0.18s of TCP connect.
// Sharing one pool lets regions reuse connections and TLS sessions.
func SharedHTTPClient() *http.Client {
	sharedOnce.Do(func() {
		t := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2: true,

			// One pool serving every region at once. The default of 2 idle
			// connections per host throttles a wide fan-out badly, and each
			// AWS region is a distinct host.
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,

			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		sharedClient = &http.Client{Transport: t, Timeout: 60 * time.Second}
	})
	return sharedClient
}
