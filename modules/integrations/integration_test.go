package integrations

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A credential must not be storable in the clear. Without a key the save fails
// rather than writing a refresh token a database dump would hand over.
func TestSavingACredentialWithoutAKeyFails(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "")
	resetKeyForTest()

	if EncryptionConfigured() {
		t.Fatal("EncryptionConfigured() is true with no key set")
	}
	if _, err := seal([]byte("hmac-secret")); !errors.Is(err, ErrNoEncryptionKey) {
		t.Fatalf("seal without a key returned %v, want ErrNoEncryptionKey", err)
	}
}

// TestSealRoundTrip and TestOpenUnderTheWrongKeyIsAnError were here, and are
// now in the platform beside the cipher they test
// (internal/kernel/security/secretbox_test.go). What they assert — that the
// plaintext is not findable in what is stored, and that a rotated key surfaces
// as an error rather than as an empty credential — is a property of the
// deployment's one cipher, which this app asks for rather than implements.
//
// What stays here is the case above: with no key at all, storing a credential
// fails instead of storing it in the clear. That is this package's behaviour,
// and the reason its screens ask before offering a form.

func TestValidateRejectsWhatCannotWork(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "key-for-validation-tests")
	resetKeyForTest()

	tests := []struct {
		name string
		req  SaveRequest
		want string
	}{
		{"unknown provider", SaveRequest{Provider: "sharepoint", Name: "x"}, "unknown integration provider"},
		{"no name", SaveRequest{Provider: ProviderWebhook, TargetURL: "https://a.mn/hook"}, "needs a name"},
		{"webhook without a url", SaveRequest{Provider: ProviderWebhook, Name: "x"}, "needs a target URL"},
		{"url with no host", SaveRequest{Provider: ProviderWebhook, Name: "x", TargetURL: "not-a-url"}, "not a valid absolute URL"},
		// A path-only URL has no host either, which is the branch it lands in.
		{"local file path", SaveRequest{Provider: ProviderWebhook, Name: "x", TargetURL: "file:///etc/passwd"}, "not a valid absolute URL"},
		// A host is present here, so this is the scheme check doing the work.
		{"non-web scheme", SaveRequest{Provider: ProviderWebhook, Name: "x", TargetURL: "ftp://files.example.mn/drop"}, "must be http or https"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			err := validate(&req)
			if err == nil {
				t.Fatalf("validate accepted %+v", tc.req)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Status says what the administrator wants, so those are the only two values it
// takes. ERROR used to be a third, written by the delivery code when something
// failed — which switched the connector off, because every selection query
// requires ACTIVE. It is not a status a caller may set either: reaching the
// CHECK constraint would answer a form submission with a raw Postgres message.
func TestValidateAcceptsOnlyTheTwoIntentStatuses(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "key-for-validation-tests")
	resetKeyForTest()

	base := func(status ConnectorStatus) SaveRequest {
		return SaveRequest{
			Provider: ProviderWebhook, Name: "Subscriber",
			TargetURL: "https://a.example.mn/hook", Status: status,
		}
	}

	// Unset means on: a connector nobody switched off is one that works.
	req := base("")
	if err := validate(&req); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if req.Status != StatusActive {
		t.Fatalf("an unset status became %q, want %s", req.Status, StatusActive)
	}

	// Case and stray whitespace come from hand-written API clients, not attacks.
	req = base(" active ")
	if err := validate(&req); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if req.Status != StatusActive {
		t.Fatalf("%q was not normalised, got %q", " active ", req.Status)
	}

	req = base(StatusInactive)
	if err := validate(&req); err != nil {
		t.Fatalf("validate rejected %s: %v", StatusInactive, err)
	}

	for _, bad := range []ConnectorStatus{"ERROR", "PAUSED", "DELETED"} {
		req = base(bad)
		err := validate(&req)
		if err == nil {
			t.Fatalf("validate accepted status %q", bad)
		}
		if !strings.Contains(err.Error(), "status must be") {
			t.Fatalf("error %q does not say which values are allowed", err)
		}
	}
}

// An OAuth connector has no URL to type. Accepting one would suggest the upload
// goes somewhere it does not.
func TestValidateClearsTheTargetURLOnOAuthProviders(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "key-for-validation-tests")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	resetKeyForTest()

	req := SaveRequest{
		Provider:  ProviderGoogleDrive,
		Name:      "Archive",
		TargetURL: "https://example.invalid/not-where-this-goes",
		Secret:    "ignored",
	}
	if err := validate(&req); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if req.TargetURL != "" || req.Secret != "" {
		t.Fatalf("OAuth connector kept url=%q secret=%q", req.TargetURL, req.Secret)
	}
}

// A provider whose OAuth client the deployment never configured must be refused
// at save time, with an error naming the variable to set.
func TestValidateRefusesAnUnconfiguredProvider(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "key-for-validation-tests")
	t.Setenv("DROPBOX_OAUTH_CLIENT_ID", "")
	t.Setenv("DROPBOX_OAUTH_CLIENT_SECRET", "")
	resetKeyForTest()

	req := SaveRequest{Provider: ProviderDropbox, Name: "Archive"}
	err := validate(&req)
	if err == nil {
		t.Fatal("validate accepted a provider with no OAuth client")
	}
	if !strings.Contains(err.Error(), "DROPBOX_OAUTH_CLIENT_ID") {
		t.Fatalf("error %q does not name the missing variable", err)
	}
}

func TestCatalogReportsAvailability(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("DROPBOX_OAUTH_CLIENT_ID", "")
	t.Setenv("DROPBOX_OAUTH_CLIENT_SECRET", "")

	byProvider := map[Provider]ProviderCatalog{}
	for _, entry := range Catalog() {
		byProvider[entry.Provider] = entry
	}

	if !byProvider[ProviderGoogleDrive].Available {
		t.Error("Google Drive is configured but reported unavailable")
	}
	if byProvider[ProviderDropbox].Available {
		t.Error("Dropbox has no client credentials but is reported available")
	}
	if !strings.Contains(byProvider[ProviderDropbox].Reason, "DROPBOX_OAUTH_CLIENT_ID") {
		t.Errorf("Dropbox reason %q does not say what is missing", byProvider[ProviderDropbox].Reason)
	}
	// A webhook needs nothing from the deployment.
	if !byProvider[ProviderWebhook].Available {
		t.Error("webhooks reported unavailable")
	}
}

func TestCapabilitiesAreProviderDriven(t *testing.T) {
	for _, tc := range []struct {
		provider Provider
		want     Capability
	}{
		{ProviderGoogleDrive, CapabilityFileExport},
		{ProviderDropbox, CapabilityFileExport},
		{ProviderGoogleMeet, CapabilityMeeting},
		{ProviderWebhook, CapabilityEventPush},
	} {
		spec, err := SpecFor(tc.provider)
		if err != nil {
			t.Fatalf("SpecFor(%s): %v", tc.provider, err)
		}
		if !spec.Supports(tc.want) {
			t.Errorf("%s does not report %s", tc.provider, tc.want)
		}
	}
	// Meet cannot store a file, and Drive cannot hold a meeting. The business
	// modules pick a connector by capability, so a wrong answer here files a
	// document into a calendar.
	drive, _ := SpecFor(ProviderGoogleDrive)
	if drive.Supports(CapabilityMeeting) {
		t.Error("Google Drive claims it can create meetings")
	}
	meet, _ := SpecFor(ProviderGoogleMeet)
	if meet.Supports(CapabilityFileExport) {
		t.Error("Google Meet claims it can store files")
	}
}

// A Meet link comes from Calendar, so the connector must ask for the Calendar
// scope. Asking for the wrong one fails only at the moment a citizen is waiting
// for a link.
func TestGoogleMeetRequestsTheCalendarScope(t *testing.T) {
	spec, err := SpecFor(ProviderGoogleMeet)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(spec.Scopes, "https://www.googleapis.com/auth/calendar.events") {
		t.Fatalf("Meet scopes %v do not include calendar.events", spec.Scopes)
	}
}

// drive.file grants access only to files this application creates. The broad
// drive scope would hand the platform the rest of someone's documents.
func TestGoogleDriveAsksForTheNarrowScope(t *testing.T) {
	spec, _ := SpecFor(ProviderGoogleDrive)
	if containsString(spec.Scopes, "https://www.googleapis.com/auth/drive") {
		t.Fatal("Drive connector asks for full-account access")
	}
	if !containsString(spec.Scopes, "https://www.googleapis.com/auth/drive.file") {
		t.Fatalf("Drive scopes %v do not include drive.file", spec.Scopes)
	}
}

// A scope short of what the code calls does not fail the build, and does not
// fail the upload either: Dropbox rejects only the call that needs the missing
// permission, so the file lands and the shared link silently never appears.
// This ties the requested scopes to the endpoints this package actually calls.
func TestDropboxRequestsAScopeForEveryEndpointItCalls(t *testing.T) {
	spec, err := SpecFor(ProviderDropbox)
	if err != nil {
		t.Fatal(err)
	}
	needs := map[string]string{
		"files/upload":                             "files.content.write",
		"users/get_current_account":                "account_info.read",
		"sharing/create_shared_link_with_settings": "sharing.write",
	}
	for endpoint, scope := range needs {
		if !containsString(spec.Scopes, scope) {
			t.Errorf("the package calls %s but never asks for %s", endpoint, scope)
		}
	}
}

func TestDropboxPath(t *testing.T) {
	tests := []struct{ folder, filename, want string }{
		{"", "contract.pdf", "/contract.pdf"},
		{"Archive", "contract.pdf", "/Archive/contract.pdf"},
		{"/Archive/", "contract.pdf", "/Archive/contract.pdf"},
		{"Archive/2026", "contract.pdf", "/Archive/2026/contract.pdf"},
		// A filename is a name, not a path: a caller-supplied "../" must not
		// climb out of the configured folder.
		{"Archive", "../../etc/passwd", "/Archive/passwd"},
		{"Archive", "", "/Archive/document"},
	}
	for _, tc := range tests {
		if got := dropboxPath(tc.folder, tc.filename); got != tc.want {
			t.Errorf("dropboxPath(%q, %q) = %q, want %q", tc.folder, tc.filename, got, tc.want)
		}
	}
}

// Dropbox carries its arguments in an HTTP header, which is ASCII. A Mongolian
// document title reaches this as a filename, so it has to be escaped or the
// request fails on an invalid header byte.
func TestEscapeNonASCII(t *testing.T) {
	got := escapeNonASCII(`{"path":"/Гэрээ.pdf"}`)
	if strings.ContainsAny(got, "ГэрээПpp") && !strings.Contains(got, `\u`) {
		t.Fatalf("non-ASCII survived unescaped: %q", got)
	}
	for _, r := range got {
		if r > 0x7F {
			t.Fatalf("result still contains a non-ASCII rune: %q", got)
		}
	}
	if !strings.Contains(got, `"path"`) {
		t.Fatalf("ASCII structure was mangled: %q", got)
	}
}

func TestURLPathEscapeHandlesCalendarIDs(t *testing.T) {
	// Calendar ids are email-like; the @ has to survive as %40 inside a path.
	if got := urlPathEscape("office@gerege.mn"); got != "office%40gerege.mn" {
		t.Fatalf("urlPathEscape gave %q", got)
	}
	if got := urlPathEscape("primary"); got != "primary" {
		t.Fatalf("urlPathEscape mangled a plain id: %q", got)
	}
}

func TestTokenExpiry(t *testing.T) {
	if (tokenBundle{}).expired() {
		t.Error("a token with no expiry is treated as expired")
	}
	if !(tokenBundle{ExpiresAt: nowMinus(time.Minute)}).expired() {
		t.Error("an expired token is treated as live")
	}
	// Refreshed a minute early: a token that expires between the check and the
	// call fails the same way, only less reproducibly.
	if !(tokenBundle{ExpiresAt: nowPlus(30 * time.Second)}).expired() {
		t.Error("a token expiring in 30s is not refreshed early")
	}
	if (tokenBundle{ExpiresAt: nowPlus(10 * time.Minute)}).expired() {
		t.Error("a token good for 10 minutes is refreshed needlessly")
	}
}

func containsString(list []string, want string) bool {
	for _, have := range list {
		if have == want {
			return true
		}
	}
	return false
}
