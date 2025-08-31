Here’s a tiny Linux-only raw epoll HTTP server in Go that uses just the standard library (syscall) and no external packages. <br>
It creates the socket, sets it non-blocking, registers it with epoll, accepts connections, reads the <br> request, and writes a minimal HTTP response—so you can see how FDs, sockets, epoll, kernel readiness notifications, <br>
and goroutines (well, here we stay single-threaded for clarity) fit together.


Of course 👍 Let me distill the big code into **concise execution steps** so you see exactly what happens:

---

### 🔹 Concise Steps of the Raw Go Epoll Server

1. **Create a listening socket**

   * `socket(AF_INET, SOCK_STREAM)` → gives a file descriptor (FD).
   * Mark it `SO_REUSEADDR` + **non-blocking**.

2. **Bind & listen**

   * `bind(0.0.0.0:8080)` → attach to port.
   * `listen()` → queue for incoming TCP connections.

3. **Create epoll instance**

   * `epoll_create1()` → gives epoll FD.
   * Register listening FD for `EPOLLIN` (read readiness).

4. **Event loop (`epoll_wait`)**

   * Kernel blocks until some FD is ready.
   * Returns list of ready FDs.

5. **If listening FD is ready**

   * Call `accept()` (non-blocking) in a loop.
   * For each new client socket → mark non-blocking → add it to epoll with `EPOLLIN | EPOLLRDHUP`.

6. **If client FD is ready**

   * Use `read(fd)` until no more data.
   * Stop once headers are read.

7. **Write HTTP response**

   * `write(fd)` with minimal HTTP headers + JSON body.
   * Close the client FD.

8. **Repeat**

   * Go back to `epoll_wait`, handle next ready FD.

---

👉 In essence:

* Kernel tells us *which FDs are ready*.
* We `accept` new clients from listen FD.
* We `read` requests from client FDs.
* We `write` back an HTTP response.
* All coordinated by **epoll** + file descriptors.
