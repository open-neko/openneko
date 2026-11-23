package webfront

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// guardedResolver is nil (the system resolver) outside tests.
var guardedResolver *net.Resolver

// guardedClient checks each address at dial time, after DNS, so a name that
// resolves to a refused address is refused too. A redirect comes back as the
// response, so an allowed URL cannot lead to another target.
func guardedClient(refuse func(net.IP) error) *http.Client {
	dialer := &net.Dialer{
		Timeout:  30 * time.Second,
		Resolver: guardedResolver,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return errors.New("OpenNeko dials only a resolved IP address")
			}
			return refuse(ip)
		},
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
