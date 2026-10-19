package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
)

// A server that closes a kept-alive connection when the next request arrives
// on it, without answering. The client repeats the request on a new one.
func TestARequestOnAClosedConnectionIsSentAgain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	const body = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	var dropped atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				rd := bufio.NewReader(conn)
				for served := 0; ; served++ {
					req, err := http.ReadRequest(rd)
					if err != nil {
						return
					}
					io.Copy(io.Discard, req.Body)
					if served == 1 {
						dropped.Add(1)
						return
					}
					fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
				}
			}(conn)
		}
	}()

	c := New("http://"+ln.Addr().String(), "", ProtocolChat)
	for i := 0; i < 3; i++ {
		res, err := c.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}}, nil)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if res.Content != "hi" {
			t.Fatalf("request %d content = %q", i+1, res.Content)
		}
	}
	if dropped.Load() == 0 {
		t.Fatal("the server never dropped a connection, so nothing was retried")
	}
}
