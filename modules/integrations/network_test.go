package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// An address inside the deployment is not a place a tenant may ask this server
// to go. The one that matters most is 169.254.169.254: on every major cloud it
// answers with the instance's own credentials, and the answer would come back
// to the administrator through last_error and the delivery log.
func TestAddressIsInternal(t *testing.T) {
	internal := []string{
		"127.0.0.1", "::1",
		"169.254.169.254",        // cloud instance metadata
		"::ffff:169.254.169.254", // the same address wearing an IPv6 hat
		"10.1.2.3", "172.16.5.9", "192.168.1.1",
		"fd00::1",       // IPv6 unique local
		"fe80::1",       // IPv6 link local
		"0.0.0.0", "::", // "this host"
		"100.64.0.1",         // carrier NAT
		"198.18.0.1",         // benchmarking
		"64:ff9b::7f00:1",    // NAT64, a route back into IPv4 space
		"::ffff:192.168.0.5", // and the mapped form of a private address
	}
	for _, raw := range internal {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if !addressIsInternal(addr) {
			t.Errorf("%s was treated as an internet address", raw)
		}
	}

	external := []string{"8.8.8.8", "203.0.113.10", "2001:4860:4860::8888"}
	for _, raw := range external {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if addressIsInternal(addr) {
			t.Errorf("%s was treated as internal, so an ordinary subscriber cannot be reached", raw)
		}
	}
}

// Save time is where an administrator can still be told. These are the cases
// certain from the text alone; a hostname is judged when it resolves.
func TestValidateRefusesTargetsInsideTheDeployment(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "key-for-validation-tests")
	t.Setenv(allowPrivateEnv, "")
	resetKeyForTest()

	refused := []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://127.0.0.1:8080/hook",
		"http://localhost:8080/hook",
		"http://api.localhost/hook",
		"http://[::1]/hook",
		"http://10.0.0.5/hook",
		"http://192.168.1.20/hook",
		"https://172.17.0.1/hook",
	}
	for _, target := range refused {
		req := SaveRequest{Provider: ProviderWebhook, Name: "Subscriber", TargetURL: target}
		if err := validate(&req); err == nil {
			t.Errorf("validate accepted %s", target)
		}
	}

	accepted := []string{
		"https://hooks.example.mn/nexus",
		"http://203.0.113.10/hook",
		"https://subscriber.gerege.mn:8443/events",
	}
	for _, target := range accepted {
		req := SaveRequest{Provider: ProviderWebhook, Name: "Subscriber", TargetURL: target}
		if err := validate(&req); err != nil {
			t.Errorf("validate rejected an ordinary subscriber %s: %v", target, err)
		}
	}
}

// A self-hosted deployment whose subscribers really are on the same private
// network is a legitimate configuration, so the guard can be turned off — but
// only on purpose, and only by whoever sets the environment.
func TestPrivateTargetsCanBeAllowedDeliberately(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "key-for-validation-tests")
	t.Setenv(allowPrivateEnv, "true")
	resetKeyForTest()

	req := SaveRequest{
		Provider: ProviderWebhook, Name: "Internal subscriber", TargetURL: "http://10.0.0.5/hook",
	}
	if err := validate(&req); err != nil {
		t.Fatalf("the deployment allowed private targets and validate still refused: %v", err)
	}
}

// The check that actually holds is at dial time, on the resolved address.
//
// Validating the URL text alone would be defeated by a hostname that resolves
// to a public address when the form is saved and to 127.0.0.1 when the event is
// delivered, and by a public URL that answers 302 with an internal one. Here
// the name is beyond reproach — the server is simply listening on loopback, as
// anything reachable only from inside would be.
func TestTheDialerRefusesAnInternalAddressWhateverTheNameSaid(t *testing.T) {
	t.Setenv(allowPrivateEnv, "")

	reached := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newHTTPClient(10 * time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("the guarded client connected to a loopback address")
	}
	if reached {
		t.Fatal("the request arrived at a server inside the deployment")
	}
	if !strings.Contains(err.Error(), "refused to connect to") {
		t.Fatalf("the error does not say why the connection was refused: %v", err)
	}

	// And with the escape hatch set, the same client reaches the same server —
	// the guard is a decision, not a broken transport.
	t.Setenv(allowPrivateEnv, "true")
	resp, err = client.Do(req.Clone(context.Background()))
	if err != nil {
		t.Fatalf("the deployment allowed private targets and the dialer still refused: %v", err)
	}
	_ = resp.Body.Close()
	if !reached {
		t.Fatal("the request did not arrive even though private targets were allowed")
	}
}
