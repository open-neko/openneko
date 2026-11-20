//go:build linux

package main

import (
	"context"
	"io"
	"net"
	"time"
)

// relayToProxy passes each connection on the loopback relay to the egress
// proxy. Commands cannot resolve the proxy's name themselves: the firewall
// drops their DNS.
func relayToProxy(ctx context.Context, listener net.Listener, proxy string) {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go relayOne(conn, proxy)
	}
}

func relayOne(conn net.Conn, proxy string) {
	defer conn.Close()
	upstream, err := net.DialTimeout("tcp", proxy, 10*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, conn)
		if c, ok := upstream.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, upstream)
		if c, ok := conn.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
