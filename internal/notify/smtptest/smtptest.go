// Package smtptest provides a minimal in-process SMTP server for tests.
package smtptest

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

// Message is one mail accepted by the fake server.
type Message struct {
	From string
	To   string
	Data string // headers and body as sent, with CRLF line endings
}

// Start runs a plaintext SMTP server on a loopback port and returns its host,
// port and a channel receiving each accepted message.
func Start(t *testing.T) (host string, port int, got <-chan Message) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtptest listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	ch := make(chan Message, 16)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(conn, ch)
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, ch
}

func serve(conn net.Conn, ch chan<- Message) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	say := func(s string) { conn.Write([]byte(s + "\r\n")) }
	say("220 smtptest ready")
	var msg Message
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250 smtptest")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			msg.From = strings.Trim(strings.TrimSpace(line[len("MAIL FROM:"):]), "<>")
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			msg.To = strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(l)
			}
			msg.Data = data.String()
			ch <- msg
			msg = Message{}
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}
