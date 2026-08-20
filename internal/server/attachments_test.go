package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KonMam/buntline/internal/session"
)

// newAttachmentTestServer builds a bare server with one session whose
// workdir is a fresh temp dir, and returns the server, session id, and
// workdir.
func newAttachmentTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(textOnlyProfileConfig(), store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(s.Shutdown)
	workdir := t.TempDir()
	meta, err := store.Create("test-model", workdir)
	if err != nil {
		t.Fatal(err)
	}
	return s, meta.ID, workdir
}

// uploadAttachment posts one file to the session's attachment endpoint.
func uploadAttachment(t *testing.T, s *Server, id, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

// waitForToolMessage polls the store until a tool message containing the
// wanted substring lands (the turn persists events asynchronously).
func waitForToolMessage(t *testing.T, store *session.Store, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := store.Messages(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Role == "tool" && strings.Contains(m.Content, want) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tool message containing %q", want)
}

// TestUploadAttachmentStoresInsideWorkdir: an uploaded file lands under
// <workdir>/.buntline/attachments/<session>/ and the response carries the
// workdir-relative path with forward slashes.
func TestUploadAttachmentStoresInsideWorkdir(t *testing.T) {
	s, id, workdir := newAttachmentTestServer(t)
	rr := uploadAttachment(t, s, id, "notes.txt", "hello attachment")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.ToSlash(filepath.Join(".buntline", "attachments", id))
	if !strings.HasPrefix(out.Path, wantPrefix+"/") {
		t.Fatalf("path = %q, want prefix %q", out.Path, wantPrefix)
	}
	abs := filepath.Join(workdir, filepath.FromSlash(out.Path))
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello attachment" {
		t.Errorf("stored content = %q, want %q", data, "hello attachment")
	}
}

// TestUploadAttachmentDedupesNames: a second upload with the same name
// gets a numeric suffix instead of overwriting the first file.
func TestUploadAttachmentDedupesNames(t *testing.T) {
	s, id, workdir := newAttachmentTestServer(t)
	if rr := uploadAttachment(t, s, id, "same.txt", "first"); rr.Code != http.StatusOK {
		t.Fatalf("first upload status = %d (body %s)", rr.Code, rr.Body.String())
	}
	rr := uploadAttachment(t, s, id, "same.txt", "second")
	if rr.Code != http.StatusOK {
		t.Fatalf("second upload status = %d (body %s)", rr.Code, rr.Body.String())
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.Path, "same-1.txt") {
		t.Fatalf("path = %q, want same-1.txt", out.Path)
	}
	data, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(out.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Errorf("stored content = %q, want %q", data, "second")
	}
	// The original is untouched.
	first, err := os.ReadFile(filepath.Join(workdir, ".buntline", "attachments", metaID(id), "same.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "first" {
		t.Errorf("original content = %q, want %q", first, "first")
	}
}

// metaID returns the session id recorded in a returned path's prefix.
func metaID(id string) string { return id }

// TestUploadAttachmentSanitizesNames: path separators and leading dots
// are stripped so the stored file cannot escape the attachments dir.
func TestUploadAttachmentSanitizesNames(t *testing.T) {
	s, id, workdir := newAttachmentTestServer(t)
	// Each name must end up stored as a clean basename (or, for a name
	// that reduces to nothing after sanitizing, be refused).
	cases := []struct {
		name string
		want string // stored basename; "" means the upload must be refused
	}{
		{"../../evil.txt", "evil.txt"},
		{"/abs/path.txt", "path.txt"},
		{".hidden.txt", "hidden.txt"},
		{"..", ""},
		{"evil.txt/..", ""},
	}
	for _, c := range cases {
		rr := uploadAttachment(t, s, id, c.name, "x")
		if c.want == "" {
			if rr.Code != http.StatusBadRequest {
				t.Errorf("upload %q status = %d, want 400 (body %s)", c.name, rr.Code, rr.Body.String())
			}
			continue
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("upload %q status = %d (body %s)", c.name, rr.Code, rr.Body.String())
		}
		var out struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(filepath.FromSlash(out.Path)) != c.want {
			t.Errorf("upload %q stored as %q, want basename %q", c.name, out.Path, c.want)
		}
	}
	attachmentsDir := filepath.Join(workdir, ".buntline", "attachments", id)
	entries, err := os.ReadDir(attachmentsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Base(e.Name()) != e.Name() {
			t.Errorf("stored entry %q escaped the attachments dir", e.Name())
		}
		if strings.Contains(e.Name(), "..") {
			t.Errorf("stored entry %q contains a dot-dot", e.Name())
		}
	}
	// Nothing was written outside the attachments dir.
	if _, err := os.Stat(filepath.Join(workdir, "evil.txt")); !os.IsNotExist(err) {
		t.Errorf("evil.txt was written outside the attachments dir")
	}
}

// TestUploadAttachmentMissingFile: a multipart body without a file part
// is a 400, not a server error.
func TestUploadAttachmentMissingFile(t *testing.T) {
	s, id, _ := newAttachmentTestServer(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
}

// TestSendMessageAttachmentOnly: an attachment with no text is a valid
// send (the read_file exchange is the content).
func TestSendMessageAttachmentOnly(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(textOnlyProfileConfig(), store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(s.Shutdown)
	workdir := t.TempDir()
	meta, err := store.Create("test-model", workdir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Profile = "deepseek"
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}

	// The uploaded attachment path is relative to the workdir; write it
	// directly (the upload endpoint itself is covered above).
	rel := filepath.Join(".buntline", "attachments", meta.ID, "a.txt")
	abs := filepath.Join(workdir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("attachment body"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"content":     "",
		"attachments": []string{filepath.ToSlash(rel)},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/messages", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rr.Code, rr.Body.String())
	}
	waitForToolMessage(t, store, meta.ID, "attachment body")
}

// TestReadAttachmentBinaryNotInlined: a PDF is reported as a binary
// notice instead of raw bytes, so the model's context never receives a
// corrupting blob.
func TestReadAttachmentBinaryNotInlined(t *testing.T) {
	workdir := t.TempDir()
	bin := filepath.Join(workdir, "doc.pdf")
	if err := os.WriteFile(bin, []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readAttachment(workdir, "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "%PDF") {
		t.Errorf("binary content leaked into the attachment text: %q", got)
	}
	if !strings.Contains(got, "binary") {
		t.Errorf("notice = %q, want a binary-file notice", got)
	}
}

// TestLooksLikeBinary covers the detector's edges: PDFs, archives, and
// UTF-16 text count as binary; plain text and JSON do not.
func TestLooksLikeBinary(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"plain", []byte("plain text"), false},
		{"json", []byte(`{"a": 1}`), false},
		{"nul", []byte{'h', 'i', 0, 'x'}, true},
		{"utf16le", []byte{0xff, 0xfe, 'h', 0}, true},
		{"pdf", []byte("%PDF-1.7\n1 0 obj\n"), true},
		{"zip", []byte("PK\x03\x04zipstuff"), true},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, true},
		{"unknown", []byte{0x01, 0x02, 0x03, 0x04, 0x05}, true},
	}
	for _, c := range cases {
		if got := looksLikeBinary(c.data); got != c.want {
			t.Errorf("looksLikeBinary(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
