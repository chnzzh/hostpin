package geoip

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLookupCacheAndRuntimeConfiguration(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"country_code":"sg","region":"Central","city":"Singapore","latitude":1.3,"longitude":103.8}`)
	}))
	defer server.Close()
	client := New(true, server.URL+"/{ip}", time.Second, time.Hour)
	for range 2 {
		location, err := client.Lookup(context.Background(), "8.8.8.8")
		if err != nil || location.CountryCode != "SG" || location.Latitude != 1.3 {
			t.Fatalf("GeoIP lookup failed: %v %#v", err, location)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("GeoIP cache made %d provider requests, want 1", requests.Load())
	}
	client.Configure(false, server.URL+"/{ip}")
	if _, err := client.Lookup(context.Background(), "1.1.1.1"); err == nil {
		t.Fatal("disabled GeoIP client performed a lookup")
	}
}

func TestLookupRejectsOversizedResponseAfterValidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"success":true,"country_code":"SG","region":"Central","city":"Singapore","latitude":1.3,"longitude":103.8}`)
		_, _ = fmt.Fprint(w, strings.Repeat(" ", 1<<20))
	}))
	defer server.Close()
	client := New(true, server.URL+"/{ip}", time.Second, time.Hour)
	if _, err := client.Lookup(context.Background(), "8.8.4.4"); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("oversized GeoIP response was not rejected: %v", err)
	}
}
