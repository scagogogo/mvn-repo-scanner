package repo

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// newHTTPClient builds an *http.Client with a tuned transport. Both the Browser
// (discovery) and Downloader (artifact fetch) share the same tuning so a single
// host Maven repository gets real connection reuse across both phases.
//
// The defaults in http.DefaultTransport set MaxIdleConnsPerHost=2, which for a
// single-host Maven repo means almost no keep-alive reuse once download
// concurrency exceeds 2 — every request re-handshakes TLS. We raise it to
// match the expected worker count and cap total idle conns so memory stays
// bounded when scanning many repos.
func newHTTPClient(timeout time.Duration, maxConnsPerHost int) *http.Client {
	if maxConnsPerHost <= 0 {
		maxConnsPerHost = 32
	}
	transport := &http.Transport{
		// Plenty of idle conns per host so concurrent workers against a single
		// Maven repo actually reuse TCP/TLS connections.
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: maxConnsPerHost,
		// Cap simultaneous connections per host so an aggressive --concurrency
		// can't open hundreds of sockets to one server.
		MaxConnsPerHost:     maxConnsPerHost,
		IdleConnTimeout:     90 * time.Second,
		// Maven Central supports HTTP/2; keep it on. For misconfigured private
		// repos that negotiate HTTP/2 poorly, the standard fallback handles it.
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}
