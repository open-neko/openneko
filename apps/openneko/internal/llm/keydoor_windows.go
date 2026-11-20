package llm

import (
	"context"
	"errors"
)

func ListenDoor(context.Context, string, *KeyDoor) (<-chan error, error) {
	return nil, errors.New("the key door needs a Unix socket; agent turns do not run on Windows yet")
}
