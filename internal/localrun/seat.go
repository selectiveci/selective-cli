package localrun

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	transportEnv = "SELECTIVE_TRANSPORT"
	socketEnv    = "SELECTIVE_SEAT_SOCKET"
	seatUsage    = "usage:\n  sel __transport --stdio\n"
	maxSocketLen = 100
)

func RunSeat(args []string, in io.Reader, out, errw io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "--help" || args[0] == "-h"):
		fmt.Fprint(errw, seatUsage)
		return 0
	case len(args) == 1 && args[0] == "--stdio":
		return relayStdio(in, out, errw)
	}
	fmt.Fprintf(errw, "unsupported arguments %q\n%s", args, seatUsage)
	return 2
}

func relayStdio(in io.Reader, out, errw io.Writer) int {
	sock := os.Getenv(socketEnv)
	if sock == "" {
		fmt.Fprintf(errw, "%s is not set; sel __transport is started by sel test\n", socketEnv)
		return 2
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	defer conn.Close()
	done := make(chan int, 2)
	go func() {
		_, err := io.Copy(out, conn)
		done <- exitCodeFor(err)
	}()
	go func() { done <- forwardLines(in, conn) }()
	return <-done
}

func forwardLines(in io.Reader, conn net.Conn) int {
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadBytes('\n')
		if bytes.Equal(bytes.TrimSpace(line), []byte("exit")) {
			return 0
		}
		if len(line) > 0 {
			if _, werr := conn.Write(line); werr != nil {
				return 1
			}
		}
		if err != nil {
			return 0
		}
	}
}

func exitCodeFor(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

type Seat struct {
	dir  string
	shim string
	sock string
	ln   net.Listener
}

func NewSeat(sel string) (*Seat, error) {
	dir, err := os.MkdirTemp(shortTempBase(), "sel")
	if err != nil {
		return nil, err
	}
	seat := &Seat{dir: dir, shim: filepath.Join(dir, "transport"), sock: filepath.Join(dir, "s.sock")}
	script := "#!/bin/sh\nexec " + shellQuote(sel) + " __transport \"$@\"\n"
	if err := os.WriteFile(seat.shim, []byte(script), 0o700); err != nil {
		seat.Close()
		return nil, err
	}
	if seat.ln, err = net.Listen("unix", seat.sock); err != nil {
		seat.Close()
		return nil, err
	}
	return seat, nil
}

func shortTempBase() string {
	base := os.TempDir()
	if len(base)+len("/sel0000000000/s.sock") > maxSocketLen {
		return "/tmp"
	}
	return base
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s *Seat) Env() []string {
	return []string{transportEnv + "=" + s.shim, socketEnv + "=" + s.sock}
}

func (s *Seat) Accept(ctx context.Context) (net.Conn, error) {
	type accepted struct {
		conn net.Conn
		err  error
	}
	result := make(chan accepted, 1)
	go func() {
		conn, err := s.ln.Accept()
		result <- accepted{conn, err}
	}()
	select {
	case a := <-result:
		return a.conn, a.err
	case <-ctx.Done():
		s.ln.Close()
		if a := <-result; a.conn != nil {
			a.conn.Close()
		}
		return nil, ctx.Err()
	}
}

func (s *Seat) Close() error {
	if s.ln != nil {
		s.ln.Close()
	}
	return os.RemoveAll(s.dir)
}
