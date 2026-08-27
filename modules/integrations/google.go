package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/google/uuid"
)

// driveUpload stores a file in the connected Google Drive.
//
// Upload is multipart/related: one JSON part naming the file and one part
// carrying the bytes, which is the single-request form Drive documents for
// files small enough not to need a resumable session. A signed PDF is.
func (m *Manager) driveUpload(ctx context.Context, accessToken string, conn *Connector,
	filename string, content []byte) (*ExportResult, error) {

	metadata := map[string]any{"name": filename}
	// folder_id is optional: without it Drive files land in the account root,
	// which is a legitimate configuration and not an error.
	if folder := strings.TrimSpace(conn.Config["folder_id"]); folder != "" {
		metadata["parents"] = []string{folder}
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	metaHeader := textproto.MIMEHeader{}
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metaPart, err := writer.CreatePart(metaHeader)
	if err != nil {
		return nil, err
	}
	if _, err := metaPart.Write(metaJSON); err != nil {
		return nil, err
	}

	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Type", contentTypeFor(filename))
	filePart, err := writer.CreatePart(fileHeader)
	if err != nil {
		return nil, err
	}
	if _, err := filePart.Write(content); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id,name,webViewLink",
		&body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	// Drive requires multipart/related here, not the multipart/form-data the
	// writer produces by default; only the boundary is reused.
	req.Header.Set("Content-Type", "multipart/related; boundary="+writer.Boundary())

	raw, err := m.doJSON(req, "Google Drive")
	if err != nil {
		return nil, err
	}

	var out struct {
		ID          string `json:"id"`
		WebViewLink string `json:"webViewLink"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable response from Google Drive: %w", err)
	}
	return &ExportResult{ExternalID: out.ID, URL: out.WebViewLink}, nil
}

// googleMeetCreate books a Meet link.
//
// There is no API that creates a Meet link on its own. A Meet conference is
// issued by Calendar as part of an event, through conferenceData.createRequest
// with conferenceDataVersion=1 — so a "Google Meet connector" is a Calendar
// grant, and the event it creates is the meeting.
func (m *Manager) googleMeetCreate(ctx context.Context, accessToken string, conn *Connector,
	title string, startsAt time.Time, duration time.Duration) (*Meeting, error) {

	if duration <= 0 {
		duration = 30 * time.Minute
	}
	calendarID := strings.TrimSpace(conn.Config["calendar_id"])
	if calendarID == "" {
		calendarID = "primary"
	}

	event := map[string]any{
		"summary": title,
		"start":   map[string]string{"dateTime": startsAt.Format(time.RFC3339)},
		"end":     map[string]string{"dateTime": startsAt.Add(duration).Format(time.RFC3339)},
		"conferenceData": map[string]any{
			"createRequest": map[string]any{
				// The request id makes the create idempotent on Google's side:
				// a retry with the same id returns the same conference instead
				// of issuing a second link for one appointment.
				"requestId":             uuid.NewString(),
				"conferenceSolutionKey": map[string]string{"type": "hangoutsMeet"},
			},
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	endpoint := fmt.Sprintf(
		"https://www.googleapis.com/calendar/v3/calendars/%s/events?conferenceDataVersion=1",
		urlPathEscape(calendarID))
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	raw, err := m.doJSON(req, "Google Calendar")
	if err != nil {
		return nil, err
	}

	var out struct {
		ID             string `json:"id"`
		HangoutLink    string `json:"hangoutLink"`
		HTMLLink       string `json:"htmlLink"`
		ConferenceData struct {
			EntryPoints []struct {
				EntryPointType string `json:"entryPointType"`
				URI            string `json:"uri"`
			} `json:"entryPoints"`
		} `json:"conferenceData"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable response from Google Calendar: %w", err)
	}

	join := out.HangoutLink
	if join == "" {
		for _, ep := range out.ConferenceData.EntryPoints {
			if ep.EntryPointType == "video" && ep.URI != "" {
				join = ep.URI
				break
			}
		}
	}
	if join == "" {
		// The event exists but carries no conference. Reporting success here
		// would put an appointment in front of a citizen with nothing to join.
		return nil, errors.New(
			"the calendar event was created but Google issued no Meet link — the account may not be " +
				"permitted to create conferences")
	}
	return &Meeting{JoinURL: join, ExternalID: out.ID}, nil
}

// providerError is a refusal from the provider rather than a failure to reach
// it. The status is kept as a number and not only as prose so that a caller can
// tell "your token is no longer valid" from "that folder does not exist" — the
// first is worth refreshing and retrying, the second is not.
type providerError struct {
	Provider string
	Status   int
	Body     string
}

func (e *providerError) Error() string {
	return fmt.Sprintf("%s answered %d: %s", e.Provider, e.Status, e.Body)
}

// doJSON performs a request and returns the body, turning a non-2xx into an
// error that carries the provider's own explanation.
func (m *Manager) doJSON(req *http.Request, provider string) ([]byte, error) {
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s unreachable: %w", provider, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, &providerError{
			Provider: provider,
			Status:   resp.StatusCode,
			Body:     strings.TrimSpace(truncate(string(body), 400)),
		}
	}
	return body, nil
}

func contentTypeFor(filename string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(filename), ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(strings.ToLower(filename), ".csv"):
		return "text/csv"
	case strings.HasSuffix(strings.ToLower(filename), ".json"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// urlPathEscape escapes a calendar id for use in a path segment. Calendar ids
// are email-like and contain @, which has to survive as %40.
func urlPathEscape(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '~' {
			b.WriteByte(r)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", r)
	}
	return b.String()
}
