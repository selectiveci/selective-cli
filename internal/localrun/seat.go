package localrun

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
)

const (
	socketEnv = "SELECTIVE_SEAT_SOCKET"
	seatUsage = "usage:\n  sel __transport --stdio\n"
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
