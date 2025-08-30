//go:build linux
// +build linux

package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// Minimal HTTP response
func writeHTTPResponse(fd int, status int, body []byte) {
	statusText := map[int]string{200: "OK", 400: "Bad Request", 500: "Internal Server Error"}[status]
	if statusText == "" {
		statusText = "OK"
	}
	header := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", status, statusText, len(body))
	// best-effort writes; ignore partials for simplicity
	// in a demo
	_, _ = syscall.Write(fd, []byte(header))
	_, _ = syscall.Write(fd, body)
}

func SetReuseAddr(fd int) error {
	/*
		Its job is to configure a network socket to allow your program to restart and re-use the same network address (IP address and port number) quickly, without waiting for a standard operating system timeout period to expire.
	*/
	return syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}

func setNonblock(fd int) error {
	/*
			In simple terms, syscall.SetNonblock(fd, true) tells the operating system: "When I ask you to read from or write to this file/socket, if you can't do it immediately, don't put my entire program to sleep waiting. Just give me an error back right away and let me carry on with other work."

		It changes the behavior of the file descriptor (fd) from blocking to non-blocking mode.
	*/
	return syscall.SetNonblock(fd, true)
}

func main() {
	port := 8080

	// 1) Create a TCP socket (IPv4)
	listenFD, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)

	if err != nil {
		log.Fatalf("Socket: %v", err)
	}
	// Ensure system will close the file descriptor
	defer syscall.Close(listenFD)

	// 2) Mark reusable
	if err := SetReuseAddr(listenFD); err != nil {
		log.Fatalf("setsockopt SO_REUSEADDR: %v", err)
	}
	if err := setNonblock(listenFD); err != nil {
		log.Fatalf("setNonblock(listen): %v", err)
	}

	// 3) Bind  to 0.0.0.0:8080
	sa := &syscall.SockaddrInet4{Port: port}
	// 0.0.0.0 already zeroed
	if err := syscall.Bind(listenFD, sa); err != nil {
		log.Fatalf("bind: %v", err)
	}

	// 4) Listen
	if err := syscall.Listen(listenFD, 1024); err != nil {
		log.Fatalf("listen: %v", err)
	}

	// 5) Create epoll instance
	epfd, err := syscall.EpollCreate1(0)
	if err != nil {
		log.Fatalf("epoll_create1: %v", err)
	}
	defer syscall.Close(epfd)

	// 6) Register the listening FD for read readiness
	var ev syscall.EpollEvent
	ev.Events = syscall.EPOLLIN // level-triggered read readiness
	ev.Fd = int32(listenFD)
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, listenFD, &ev); err != nil {
		log.Fatalf("epoll_ctl ADD listenFD: %v", err)
	}

	// Graceful shutdown handling
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)

	log.Printf("Listening on http://0.0.0.0:%d (raw epoll)\n", port)

	// 7) Event Loop
	events := make([]syscall.EpollEvent, 128)
	for {
		select {
		case <-sigc:
			log.Println("shutting down...")
			return
		default:
		}

		n, err := syscall.EpollWait(epfd, events, -1)
		if err != nil {
			// Epoll can be interrupted by signals
			if err == syscall.EINTR {
				continue
			}
			log.Fatalf("epoll_wait: %v", err)
		}

		for i := 0; i < n; i++ {
			e := events[i]
			fd := int(e.Fd)

			if fd == listenFD {
				// 8) Accept as many connecctions as are
				// queued
				connFD, _, err := syscall.Accept(listenFD)
				if err != nil {
					if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
						// no more to accept
						break
					}
					log.Printf("accept error: %v", err)
					break
				}

				// Non-blocking client socket
				_ = setNonblock(connFD)

				// Register for read = hangup notification
				var cev syscall.EpollEvent
				cev.Events = syscall.EPOLLIN | syscall.EPOLLRDHUP
				cev.Fd = int32(connFD)

				if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, connFD, &cev); err != nil {
					log.Printf("epoll_ctl ADD connFD: %v", err)
					syscall.Close(connFD)
					continue
				}
			} else {
				// 9) Handle client socket readiness
				// Read until EAGAIN (level-triggered)
				var reqBuf bytes.Buffer
				tmp := make([]byte, 4096)
				for {
					n, rerr := syscall.Read(fd, tmp)
					if n > 0 {
						reqBuf.Write(tmp[:n])

						// Simple HTTP parser break
						// condition: headers end
						if bytes.Contains(reqBuf.Bytes(), []byte("\r\n\r\n")) {
							// for demo, stop reading early; we return a small
							// response
							break
						}
					}
					if rerr != nil {
						if rerr == syscall.EAGAIN || rerr == syscall.EWOULDBLOCK {
							break
						}
						// client closed or error
						syscall.Close(fd)
						goto nextEvent
					}

					if n == 0 {
						// Remote closed
						syscall.Close(fd)
						goto nextEvent
					}
				}

				// 10) Minimal routing: respond JSON to
				// any request
				body := []byte(`{"message":"Hello from raw epoll Go server","remote":"` + peerString(fd) + `"}`)
				writeHTTPResponse(fd, 200, body)
				syscall.Close(fd) // close (no keep-alive)
			}
		nextEvent:
		}
	}
}

func peerString(fd int) string {
	sa, err := syscall.Getpeername(fd)
	if err != nil {
		return "?"
	}
	switch v := sa.(type) {
	case *syscall.SockaddrInet4:
		ip := net.IPv4(v.Addr[0], v.Addr[1], v.Addr[2], v.Addr[3]).String()
		return fmt.Sprintf("%s:%d", ip, v.Port)
	case *syscall.SockaddrInet6:
		ip := net.IP(v.Addr[:]).String()
		return fmt.Sprintf("[%s]:%d", ip, v.Port)
	default:
		return "?"
	}
}

/*
here’s a tiny Linux-only raw epoll HTTP server in Go that uses just the standard library (syscall) and no external packages.
It creates the socket, sets it non-blocking, registers it with epoll, accepts connections, reads the request,
and writes a minimal HTTP response—so you can see how FDs, sockets, epoll, kernel readiness notifications,
and goroutines (well, here we stay single-threaded for clarity) fit together.
*/
