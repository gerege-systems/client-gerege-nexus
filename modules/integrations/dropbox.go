package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

// dropboxUpload stores a file in the connected Dropbox.
//
// Dropbox splits its API in two hosts: api.dropboxapi.com for JSON calls and
// content.dropboxapi.com for bytes. On the content host the arguments travel in
// the Dropbox-API-Arg header and the body is the file itself — which is why
// this does not look like the Drive upload.
func (m *Manager) dropboxUpload(ctx context.Context, accessToken string, conn *Connector,
	filename string, content []byte) (*ExportResult, error) {

	dest := dropboxPath(conn.Config["folder_path"], filename)

	arg, err := json.Marshal(map[string]any{
		"path": dest,
		// "add" rather than "overwrite": two documents signed in the same
		// second must not silently become one. Dropbox renames the second.
		"mode":            "add",
		"autorename":      true,
		"mute":            true,
		"strict_conflict": false,
	})
	if err != nil {
		return nil, err
	}
	// The header must be HTTP-safe ASCII; Dropbox documents \uXXXX escaping for
	// anything else, which matters here because a Mongolian document title
	// reaches this as a filename.
	argHeader := escapeNonASCII(string(arg))

	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://content.dropboxapi.com/2/files/upload", bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Dropbox-API-Arg", argHeader)
	req.Header.Set("Content-Type", "application/octet-stream")

	raw, err := m.doJSON(req, "Dropbox")
	if err != nil {
		return nil, err
	}

	var out struct {
		ID          string `json:"id"`
		PathDisplay string `json:"path_display"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unreadable response from Dropbox: %w", err)
	}

	res := &ExportResult{ExternalID: out.ID}
	// A shared link is a second call and can fail on its own; the upload has
	// already succeeded by this point, so its failure is not the export's.
	if link, err := m.dropboxSharedLink(ctx, accessToken, out.PathDisplay); err == nil {
		res.URL = link
	}
	return res, nil
}

// dropboxSharedLink returns a view link for an uploaded file, reusing the
// existing one when Dropbox says a link already exists.
func (m *Manager) dropboxSharedLink(ctx context.Context, accessToken, filePath string) (string, error) {
	if filePath == "" {
		return "", errors.New("no path to share")
	}
	body, err := json.Marshal(map[string]any{"path": filePath})
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		"https://api.dropboxapi.com/2/sharing/create_shared_link_with_settings",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	raw, err := m.doJSON(req, "Dropbox")
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// dropboxPath builds the destination path. Dropbox wants a leading slash and
// rejects a trailing one, and an empty folder means the account root — which is
// written as "" rather than "/".
func dropboxPath(folder, filename string) string {
	folder = strings.Trim(strings.TrimSpace(folder), "/")
	name := path.Base(strings.TrimSpace(filename))
	if name == "" || name == "." || name == "/" {
		name = "document"
	}
	if folder == "" {
		return "/" + name
	}
	return "/" + folder + "/" + name
}

// escapeNonASCII rewrites non-ASCII runes as \uXXXX, the form Dropbox specifies
// for its header arguments. Without it a Cyrillic filename makes the request
// fail on an invalid header byte rather than on anything to do with Dropbox.
func escapeNonASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r1, r2 := surrogates(r)
			fmt.Fprintf(&b, "\\u%04x\\u%04x", r1, r2)
			continue
		}
		fmt.Fprintf(&b, "\\u%04x", r)
	}
	return b.String()
}

// surrogates splits an astral rune into the UTF-16 pair \uXXXX escaping needs.
func surrogates(r rune) (rune, rune) {
	r -= 0x10000
	return 0xD800 + (r >> 10), 0xDC00 + (r & 0x3FF)
}
