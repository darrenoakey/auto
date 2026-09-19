// porthelper is a test-only service binary for the overlap-restart tests. It
// binds 127.0.0.1:<port>, optionally with SO_REUSEPORT, accepts connections
// and closes them, and exits nonzero when its bind fails. Compiled once by
// TestMain so each test spawns a deterministic fast-starting child instead of
// an interpreter.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"syscall"
)

func main() {
	reuse := flag.Bool("reuse", false, "hold the listener with SO_REUSEPORT")
	port := flag.Int("port", 0, "TCP port to bind on 127.0.0.1")
	flag.Parse()

	var listenConfig net.ListenConfig
	if *reuse {
		listenConfig.Control = func(network, address string, conn syscall.RawConn) error {
			var controlErr error
			if err := conn.Control(func(fd uintptr) {
				controlErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return controlErr
		}
	}
	listener, err := listenConfig.Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fmt.Printf("BINDFAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("READY")
	for {
		connection, err := listener.Accept()
		if err != nil {
			os.Exit(0)
		}
		_ = connection.Close()
	}
}
