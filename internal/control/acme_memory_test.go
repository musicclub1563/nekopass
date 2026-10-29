package control

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedPrivateACMEJobClosesItsIdleConnections(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	var node int64
	if err := f.p.QueryRow(ctx, "INSERT INTO nodes(name,token_hash) VALUES($1,$2) RETURNING id", Secret(), Secret()).Scan(&node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.p.Exec(ctx, "DELETE FROM nodes WHERE id=$1", node) })
	var live, requests atomic.Int64
	directory := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Replay-Nonce", Secret())
		w.Header().Set("Content-Type", "application/json")
		base := "https://" + r.Host
		switch r.URL.Path {
		case "/directory":
			json.NewEncoder(w).Encode(map[string]string{"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/order", "revokeCert": base + "/revoke", "keyChange": base + "/key-change"})
		case "/nonce":
			w.WriteHeader(200)
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:acme:error:malformed", "detail": "fixture failure", "status": 400})
		}
	}))
	directory.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			live.Add(1)
		}
		if state == http.StateClosed {
			live.Add(-1)
		}
	}
	directory.StartTLS()
	defer directory.Close()
	cfg := DefaultTunnelTLS()
	cfg.ServerName = "fixture.example.test"
	cfg.CertificateMode = "acme_http"
	cfg.ACMEEmail = "fixture@example.test"
	cfg.ACMEDirectory = directory.URL + "/directory"
	cfg.ACMERootCA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: directory.Certificate().Raw}))
	for range 3 {
		if err := f.s.issueNodeCertificate(ctx, node, 1, cfg); err == nil {
			t.Fatal("fixture unexpectedly issued a certificate")
		}
		until := time.Now().Add(2 * time.Second)
		for live.Load() != 0 && time.Now().Before(until) {
			time.Sleep(10 * time.Millisecond)
		}
		if live.Load() != 0 {
			t.Fatal("failed private ACME attempt retained its HTTP transport connections", live.Load())
		}
	}
	if requests.Load() < 3 {
		t.Fatal("test never exercised ACME HTTP connections")
	}
}
