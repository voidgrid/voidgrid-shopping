package mailer

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

// fakeSMTP speaks just enough SMTP (no TLS, no auth) to capture one message.
func fakeSMTP(t *testing.T) (port int, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		w("220 fake")
		var data strings.Builder
		in := false
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimRight(l, "\r\n")
			switch {
			case in && l == ".":
				in = false
				got <- data.String()
				w("250 ok")
			case in:
				data.WriteString(l + "\n")
			case strings.HasPrefix(l, "EHLO"), strings.HasPrefix(l, "HELO"):
				w("250 fake")
			case l == "DATA":
				in = true
				w("354 go")
			case l == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

func TestSend(t *testing.T) {
	port, got := fakeSMTP(t)
	c := Config{Host: "127.0.0.1", Port: port, Security: None, From: "Shopping <shop@example.com>"}
	if err := Send(context.Background(), c, "a@example.com", "Hello", "line one\nline two"); err != nil {
		t.Fatal(err)
	}
	m := <-got
	for _, want := range []string{"Subject: Hello", "To: <a@example.com>", "From: \"Shopping\" <shop@example.com>", "line two"} {
		if !strings.Contains(m, want) {
			t.Errorf("missing %q in %q", want, m)
		}
	}
}

func TestSendRefusesBadInput(t *testing.T) {
	c := Config{Host: "127.0.0.1", Port: 1, Security: None, From: "shop@example.com"}
	if err := Send(context.Background(), c, "a@example.com", "x\r\nBcc: evil@example.com", "b"); err == nil {
		t.Error("header injection in subject accepted")
	}
	if err := Send(context.Background(), c, "not an address", "x", "b"); err == nil {
		t.Error("bad recipient accepted")
	}
	if err := Send(context.Background(), Config{}, "a@example.com", "x", "b"); err == nil {
		t.Error("unconfigured accepted")
	}
	if err := Send(context.Background(), c, "a@example.com", "x", "b"); err == nil {
		t.Error("closed port accepted")
	}
}
