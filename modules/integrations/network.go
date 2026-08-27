package integrations

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// Whoever can save a connector chooses an address this server then connects to,
// with this server's network position. That is the whole shape of a server-side
// request forgery: a tenant administrator — who is not the operator of the
// deployment — types http://169.254.169.254/latest/meta-data/ into a webhook
// target and the platform fetches the cloud instance's credentials for them.
// The status and the provider's own words come back through last_error and the
// delivery log, so it is not even blind.
//
// The check that matters is at dial time, not at save time. A hostname resolves
// when the connection is made, so a name that answered with a public address
// when the form was submitted can answer with 127.0.0.1 an hour later, and a
// redirect can land anywhere. Control runs after resolution, on the concrete
// address, on every attempt and on every hop — which is the only place the
// decision is about where the socket is actually going.

// allowPrivateEnv lets a deployment turn the guard off.
//
// A self-hosted installation whose subscribers genuinely live on the same
// private network is a real configuration, and refusing to serve it would push
// the operator to a worse workaround. It is off by default because the
// deployments that need it know they do.
const allowPrivateEnv = "INTEGRATION_ALLOW_PRIVATE_TARGETS"

func allowPrivateTargets() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowPrivateEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// reservedPrefixes are the ranges netip's own predicates do not name.
var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // carrier NAT, RFC 6598
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),  // documentation
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64, a way to reach IPv4 space
}

// addressIsInternal reports whether an address belongs to the deployment rather
// than to the internet. An address that cannot be parsed counts as internal:
// the failure mode of this function has to be refusal.
func addressIsInternal(addr netip.Addr) bool {
	// ::ffff:127.0.0.1 is 127.0.0.1 wearing a hat.
	addr = addr.Unmap()
	if !addr.IsValid() {
		return true
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// newHTTPClient builds the client every outbound call in this package uses.
//
// The guard sits on the transport rather than on the callers, so a connector
// added later cannot forget it, and so it covers the addresses no caller ever
// sees: the second answer of a round-robin DNS name, and each hop of a
// redirect chain.
//
// One consequence to know about: the transport keeps ProxyFromEnvironment, and
// a dial through a proxy is a dial to the proxy. A deployment that reaches the
// internet through an HTTP proxy on its own network has to set
// INTEGRATION_ALLOW_PRIVATE_TARGETS, because from here that proxy is
// indistinguishable from the thing this refuses to talk to.
func newHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivateTargets() {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("refused to connect to %q: it is not an address", address)
			}
			addr, err := netip.ParseAddr(host)
			if err != nil || addressIsInternal(addr) {
				return fmt.Errorf(
					"refused to connect to %s: it is inside the deployment's own network, "+
						"not on the internet", host)
			}
			return nil
		},
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	return &http.Client{Timeout: timeout, Transport: transport}
}

// checkTargetHost refuses a target an administrator can be told about now,
// rather than leaving them to read it out of a delivery failure later.
//
// It is deliberately only the cases that are certain from the text itself. A
// hostname is not resolved here: the answer would be advisory — it can change
// before the first delivery — and a lookup inside a form submission is a
// timeout waiting to happen. The dialer above is what actually enforces this.
func checkTargetHost(host string) error {
	if allowPrivateTargets() {
		return nil
	}
	name := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if name == "" {
		return invalid("the target URL is not a valid absolute URL")
	}
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return invalid("the target URL points at this server itself")
	}
	// A host that does not parse as an address is a name, and whether a name is
	// internal is decided when it resolves — by the dialer, not here.
	if addr, err := netip.ParseAddr(name); err == nil && addressIsInternal(addr) {
		return invalid(
			"the target URL points inside the deployment's own network (%s), which this server "+
				"will not be asked to reach on a tenant's behalf", addr)
	}
	return nil
}
