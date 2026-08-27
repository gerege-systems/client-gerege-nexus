package integrations

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrProviderUnavailable is a provider this deployment cannot offer because
// nobody has registered an OAuth client for it. It is a gap in the deployment,
// not in the request, and the message names the variables to set.
var ErrProviderUnavailable = errors.New("this provider is not configured for this deployment")

// Provider identifies what a connector talks to.
type Provider string

const (
	// The original four: a target URL and, for webhooks, a signing secret.
	ProviderWebhook    Provider = "webhook"
	ProviderGovernment Provider = "government"
	ProviderPayment    Provider = "payment"
	ProviderCustomREST Provider = "custom_rest"

	// The SaaS connectors, which are reached with an OAuth grant rather than a
	// URL an operator types in.
	ProviderGoogleDrive Provider = "google_drive"
	ProviderDropbox     Provider = "dropbox"
	ProviderGoogleMeet  Provider = "google_meet"
)

// Capability is what a connector can be asked to do. It is what the business
// modules select a connector by: the esign module wants somewhere to file a
// PDF, not "a Dropbox".
type Capability string

const (
	// CapabilityFileExport: give it bytes and a filename, it stores them.
	CapabilityFileExport Capability = "file_export"
	// CapabilityMeeting: give it a title and a time, it returns a join URL.
	CapabilityMeeting Capability = "meeting"
	// CapabilityEventPush: give it an event, it POSTs the JSON.
	CapabilityEventPush Capability = "event_push"
)

// Spec describes one provider: how it authenticates, what it can do, and what
// the deployment must supply before it can be offered at all.
type Spec struct {
	Provider Provider
	// OAuth is false for the URL-and-secret connectors.
	OAuth        bool
	Capabilities []Capability

	// AuthURL / TokenURL / Scopes are meaningful only when OAuth is true.
	AuthURL  string
	TokenURL string
	Scopes   []string

	// ClientIDEnv / ClientSecretEnv name the deployment credentials. These are
	// per-deployment rather than per-tenant on purpose: an OAuth client is
	// registered against a consent screen the operator of this platform owns,
	// and asking every tenant administrator to create their own Google Cloud
	// project is not a product.
	ClientIDEnv     string
	ClientSecretEnv string
}

var specs = map[Provider]Spec{
	ProviderWebhook:    {Provider: ProviderWebhook, Capabilities: []Capability{CapabilityEventPush}},
	ProviderGovernment: {Provider: ProviderGovernment},
	ProviderPayment:    {Provider: ProviderPayment},
	ProviderCustomREST: {Provider: ProviderCustomREST},

	ProviderGoogleDrive: {
		Provider:     ProviderGoogleDrive,
		OAuth:        true,
		Capabilities: []Capability{CapabilityFileExport},
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		// drive.file is deliberately narrow: it grants access only to files
		// this application creates, so connecting a Drive does not hand the
		// platform the rest of someone's documents.
		Scopes:          []string{"https://www.googleapis.com/auth/drive.file", "openid", "email"},
		ClientIDEnv:     "GOOGLE_OAUTH_CLIENT_ID",
		ClientSecretEnv: "GOOGLE_OAUTH_CLIENT_SECRET",
	},

	ProviderGoogleMeet: {
		Provider:     ProviderGoogleMeet,
		OAuth:        true,
		Capabilities: []Capability{CapabilityMeeting},
		AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		// There is no standalone Meet API for creating a meeting. A Meet link
		// is issued by Calendar as part of an event, through
		// conferenceData.createRequest — so the scope a "Meet connector" needs
		// is the Calendar one.
		Scopes:          []string{"https://www.googleapis.com/auth/calendar.events", "openid", "email"},
		ClientIDEnv:     "GOOGLE_OAUTH_CLIENT_ID",
		ClientSecretEnv: "GOOGLE_OAUTH_CLIENT_SECRET",
	},

	ProviderDropbox: {
		Provider:     ProviderDropbox,
		OAuth:        true,
		Capabilities: []Capability{CapabilityFileExport},
		AuthURL:      "https://www.dropbox.com/oauth2/authorize",
		TokenURL:     "https://api.dropbox.com/oauth2/token",
		// Dropbox scopes are granular, and each of these is asked for because
		// something here calls it: files.content.write to upload,
		// account_info.read to name the connected account, sharing.write for
		// the shared link recorded against the delivery. Requesting a scope
		// short of what the code calls is not a build error — the upload
		// succeeds and the link silently never appears — so the list is
		// pinned by a test against the endpoints actually used.
		Scopes:          []string{"files.content.write", "account_info.read", "sharing.write"},
		ClientIDEnv:     "DROPBOX_OAUTH_CLIENT_ID",
		ClientSecretEnv: "DROPBOX_OAUTH_CLIENT_SECRET",
	},
}

// SpecFor returns the provider description, or an error naming the provider so
// a bad value in a request body reads as a bad value and not as a 500.
func SpecFor(p Provider) (Spec, error) {
	spec, ok := specs[p]
	if !ok {
		return Spec{}, invalid("unknown integration provider %q", p)
	}
	return spec, nil
}

// Supports reports whether the provider offers a capability.
func (s Spec) Supports(c Capability) bool {
	for _, have := range s.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// Credentials returns the deployment's OAuth client for this provider.
func (s Spec) Credentials() (id, secret string, err error) {
	if !s.OAuth {
		return "", "", fmt.Errorf("provider %s does not use OAuth", s.Provider)
	}
	id = strings.TrimSpace(os.Getenv(s.ClientIDEnv))
	secret = strings.TrimSpace(os.Getenv(s.ClientSecretEnv))
	if id == "" || secret == "" {
		// Wrapped rather than plain, because the API layer answers this
		// differently: it is not the administrator's form that is wrong, it is
		// the deployment that has not been given a client to speak with.
		return "", "", fmt.Errorf("%w: provider %s needs %s and %s",
			ErrProviderUnavailable, s.Provider, s.ClientIDEnv, s.ClientSecretEnv)
	}
	return id, secret, nil
}

// Available reports whether the deployment can offer this provider at all. The
// integrations screen greys out what it cannot connect rather than letting an
// administrator fill in a form that was never going to work.
func (s Spec) Available() bool {
	if !s.OAuth {
		return true
	}
	_, _, err := s.Credentials()
	return err == nil
}

// ProviderCatalog is what the UI lists.
type ProviderCatalog struct {
	Provider     Provider     `json:"provider"`
	OAuth        bool         `json:"oauth"`
	Capabilities []Capability `json:"capabilities"`
	Available    bool         `json:"available"`
	// Reason is empty when Available; otherwise it says what is missing, in
	// terms of the environment variable an operator has to set.
	Reason string `json:"reason,omitempty"`
}

// Catalog lists every provider with whether this deployment can offer it.
func Catalog() []ProviderCatalog {
	order := []Provider{
		ProviderWebhook, ProviderGovernment, ProviderPayment, ProviderCustomREST,
		ProviderGoogleDrive, ProviderDropbox, ProviderGoogleMeet,
	}
	out := make([]ProviderCatalog, 0, len(order))
	for _, p := range order {
		spec := specs[p]
		entry := ProviderCatalog{
			Provider:     p,
			OAuth:        spec.OAuth,
			Capabilities: spec.Capabilities,
			Available:    spec.Available(),
		}
		if !entry.Available {
			if _, _, err := spec.Credentials(); err != nil {
				entry.Reason = err.Error()
			}
		}
		if entry.Capabilities == nil {
			entry.Capabilities = []Capability{}
		}
		out = append(out, entry)
	}
	return out
}
