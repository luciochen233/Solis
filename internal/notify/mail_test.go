package notify

import (
	"strings"
	"testing"
	"time"

	"Solis/internal/notify/smtptest"
)

func TestSendDeliversMessage(t *testing.T) {
	host, port, got := smtptest.Start(t)
	m := Mailer{Host: host, Port: port, From: "Solis <solis@example.test>"}
	if err := m.Send("owner@example.test", "Found: Blue umbrella (AB12CD)", "line one\nline two\n"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case msg := <-got:
		if msg.From != "solis@example.test" || msg.To != "owner@example.test" {
			t.Errorf("envelope: from %q to %q", msg.From, msg.To)
		}
		for _, want := range []string{"Subject: Found: Blue umbrella (AB12CD)\r\n", "To: owner@example.test\r\n", "\r\n\r\nline one\r\nline two\r\n"} {
			if !strings.Contains(msg.Data, want) {
				t.Errorf("message lacks %q:\n%s", want, msg.Data)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
}

// Nothing placed in a header may add a header of its own.
func TestBuildMessageBlocksHeaderInjection(t *testing.T) {
	msg := string(buildMessage("a@example.test", "b@example.test", "Found: x\r\nBcc: evil@example.test", "body", time.Unix(0, 0).UTC()))
	head, _, _ := strings.Cut(msg, "\r\n\r\n")
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("injected header survived: %q", line)
		}
	}
}

// Mail must never go out in the clear to a remote server.
func TestSendRefusesPlaintextToRemoteHost(t *testing.T) {
	if isLocal("mail.example.test") || !isLocal("127.0.0.1") || !isLocal("localhost") || !isLocal("::1") {
		t.Fatal("isLocal misclassifies hosts")
	}
	if (Mailer{}).Configured() || (Mailer{Host: "h", Port: 587}).Configured() {
		t.Fatal("a mailer without host/port/from must not count as configured")
	}
	if err := (Mailer{}).Send("a@example.test", "s", "b"); err == nil {
		t.Fatal("Send on an unconfigured mailer should fail")
	}
}
