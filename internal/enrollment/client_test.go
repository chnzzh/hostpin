package enrollment

import "testing"

func TestEndpointAndPlainHTTPPolicy(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:8080", "http://10.0.0.4:8080", "http://[::1]:8080", "http://localhost:8080"} {
		if !IsSafePlainHTTP(endpoint) {
			t.Errorf("safe local endpoint rejected: %s", endpoint)
		}
	}
	for _, endpoint := range []string{"http://198.51.100.4", "http://example.com"} {
		if IsSafePlainHTTP(endpoint) {
			t.Errorf("public plain-HTTP endpoint accepted: %s", endpoint)
		}
	}
	if _, err := NormalizeEndpoint("https://user:secret@example.com"); err == nil {
		t.Fatal("endpoint credentials were accepted")
	}
}
