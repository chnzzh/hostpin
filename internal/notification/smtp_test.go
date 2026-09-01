package notification

import "testing"

func TestSMTPAddressAndHeaderValidation(t *testing.T) {
	address, err := parseSMTPAddress("Hostpin Alerts <alerts@example.com>")
	if err != nil || address.Address != "alerts@example.com" {
		t.Fatalf("valid mailbox was rejected: %#v, %v", address, err)
	}
	if _, err := parseSMTPAddress("alerts@example.com\r\nBcc: victim@example.com"); err == nil {
		t.Fatal("SMTP header injection address was accepted")
	}
	if got := sanitizeHeaderValue("edge\r\nBcc: victim@example.com"); got != "edge  Bcc: victim@example.com" {
		t.Fatalf("unexpected sanitized header: %q", got)
	}
}
