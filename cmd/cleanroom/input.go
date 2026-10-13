package main

import (
	"bufio"
	"context"
	"errors"
	"io"
)

// commandInput is what a person types at a command's prompts. Every read
// ends when the command's context is cancelled, so no prompt needs its own
// handling of Ctrl-C.
type commandInput struct {
	ctx context.Context
	in  *bufio.Reader
	// password reads a line without showing it.
	password func() (string, error)
}

func (in commandInput) ReadLine() (string, error) {
	return await(in.ctx, func() (string, error) {
		return entered(in.in.ReadString('\n'))
	})
}

// entered is the line a person entered, given what reading up to a newline
// returned. Input that ends before Enter was pressed is not an entry, and
// gives an empty line.
func entered(line string, err error) (string, error) {
	if errors.Is(err, io.EOF) {
		return "", nil
	}
	return line, err
}

func (in commandInput) ReadPassword() (string, error) {
	return await(in.ctx, in.password)
}

// await returns what read returns, or the context's error if the context
// is cancelled first, including while read still holds a value it has not
// handed over. A goroutine cannot be stopped from outside, so a cancelled
// read stays blocked until its input closes or the process exits.
func await[T any](ctx context.Context, read func() (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	var none T
	if err := ctx.Err(); err != nil {
		return none, err
	}
	done := make(chan result, 1)
	go func() {
		value, err := read()
		done <- result{value, err}
	}()
	select {
	case <-ctx.Done():
		return none, ctx.Err()
	case r := <-done:
		if err := ctx.Err(); err != nil {
			return none, err
		}
		return r.value, r.err
	}
}
