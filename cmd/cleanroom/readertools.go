package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"

	"cleanroom/internal/reader"
)

// readerToolsCommand joins stdin and stdout to a unix socket. A command
// reader is given an MCP config file that names this command, and connects
// to it as any MCP client would. At the other end of the socket is the
// cleanroom server that asked the question, serving that one reader's
// tools over the upstream connections it already holds.
//
// The socket's path arrives in the environment and not as an argument, so
// that the command line is the same for every reader. A user who lets
// Claude Code start only listed commands can then list this one.
//
// This command reads no policy and no log and starts nothing. Everything
// that decides what a reader may do happens in the server.
func readerToolsCommand(fs *flag.FlagSet, c *common) func(context.Context) error {
	return func(ctx context.Context) error {
		socket := os.Getenv(reader.SocketEnv)
		if fs.NArg() != 0 || socket == "" {
			return errors.New("this command is started by cleanroom, which names the socket in " + reader.SocketEnv)
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return err
		}
		defer conn.Close()
		go func() {
			io.Copy(conn, c.stdin)
			// The reader has finished talking. Tell the server, which
			// then closes its side and ends the copy below.
			conn.(*net.UnixConn).CloseWrite()
		}()
		_, err = io.Copy(c.stdout, conn)
		return err
	}
}
