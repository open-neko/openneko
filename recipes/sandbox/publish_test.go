//go:build linux

package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestPublishRelaysToTheSandboxAndStops(t *testing.T) {
	if err := publish(context.Background(), "127.0.0.1:0", "openneko-sandbox"); err == nil {
		t.Fatal("publish accepted a target with no port")
	}
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		_, _ = io.ReadFull(conn, buf)
		_, _ = conn.Write([]byte("sandbox " + string(buf)))
	}()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := free.Addr().String()
	_ = free.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- publish(ctx, listen, backend.Addr().String()) }()
	var conn net.Conn
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if conn, err = net.Dial("tcp", listen); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("ping"))
	answer, err := io.ReadAll(conn)
	conn.Close()
	if err != nil || string(answer) != "sandbox ping" {
		t.Fatalf("answer = %q, %v", answer, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publish did not stop when its context ended")
	}
	if _, err := net.DialTimeout("tcp", listen, time.Second); err == nil {
		t.Fatal("the forward port still accepts after the stop")
	}
}
