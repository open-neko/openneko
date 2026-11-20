//go:build linux

package main

import (
	"context"
	"io"
	"net"
	"time"
)

// relayTo passes each connection on listener to target until ctx ends. The
// runs' relay names the egress proxy: commands cannot resolve the proxy's
// name themselves, because the firewall drops their DNS.
func relayTo(ctx context.Context, listener net.Listener, target string) {
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
		go relayOne(conn, target)
	}
}

func relayOne(conn net.Conn, target string) {
	defer conn.Close()
	upstream, err := net.DialTimeout("tcp", target, 10*time.Second)
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
