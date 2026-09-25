package gateway

import (
	"net"
	"net/http"
	"time"
)

// newTransport builds the single *http.Transport shared by every upstream's
// reverse proxy. One shared transport is enough since Go's transport
// already pools idle connections per remote host internally.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil, // never honor HTTP_PROXY/HTTPS_PROXY when reaching upstreams
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
