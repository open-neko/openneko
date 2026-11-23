package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	maxNameLine     = 64
	maxBrokerAnswer = 4 << 10
)

// readNameLine reads one short line a byte at a time, so no byte after it
// is taken from the connection.
func readNameLine(r io.Reader, most int) (string, error) {
	var b strings.Builder
	one := make([]byte, 1)
	for b.Len() <= most {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return b.String(), nil
		}
		b.WriteByte(one[0])
	}
	return "", errors.New("the line is too long")
}

// socketBroker is the runner's end of the broker.
type socketBroker struct{ path string }

func (b socketBroker) dialer(name string) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "unix", b.path)
		if err != nil {
			return nil, fmt.Errorf("the broker: %w", err)
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = c.SetDeadline(deadline)
		}
		var answer string
		if _, err = c.Write([]byte(name + "\n")); err == nil {
			answer, err = readNameLine(c, maxBrokerAnswer)
		}
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("the broker gave no answer: %v", err)
		}
		if answer != "+" {
			_ = c.Close()
			return nil, errors.New(strings.TrimSpace(strings.TrimPrefix(answer, "-")))
		}
		_ = c.SetDeadline(time.Time{})
		return c, nil
	}
}
