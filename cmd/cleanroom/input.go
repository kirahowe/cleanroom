package main

import (
	"bufio"
	"context"
)

// commandInput makes every CLI prompt interruptible, including when the
// next line is already buffered. A pending read ends when stdin closes or
// the process exits.
type commandInput struct {
	ctx context.Context
	in  *bufio.Reader
}

func (in commandInput) ReadLine() (string, error) {
	if err := in.ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := in.in.ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case <-in.ctx.Done():
		return "", in.ctx.Err()
	case r := <-done:
		if err := in.ctx.Err(); err != nil {
			return "", err
		}
		return r.line, r.err
	}
}
