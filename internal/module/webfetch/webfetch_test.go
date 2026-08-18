package webfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fetch(t *testing.T, url string) (string, error) {
	t.Helper()
	tool := &Tool{Client: http.DefaultClient}
	args, _ := json.Marshal(map[string]string{"url": url})
	res, err := tool.Run(context.Background(), args)
	return res.Content, err
}

func TestFetchStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>x</title><style>body{color:red}</style></head>
<body><script>alert(1)</script><h1>Title</h1><p>First para.</p><p>Second &amp; third.</p></body></html>`))
	}))
	defer srv.Close()

	out, err := fetch(t, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Title", "First para.", "Second & third."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	for _, banned := range []string{"alert(1)", "color:red", "<p>"} {
		if strings.Contains(out, banned) {
			t.Errorf("should be stripped: %q in %q", banned, out)
		}
	}
}

func TestFetchPlainTextPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("plain <b>not html</b> content"))
	}))
	defer srv.Close()

	out, err := fetch(t, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<b>not html</b>") {
		t.Errorf("plain text should not be tag-stripped: %q", out)
	}
}

func TestFetchRejectsNonHTTP(t *testing.T) {
	if _, err := fetch(t, "file:///etc/passwd"); err == nil {
		t.Error("file: URL should be rejected")
	}
	if _, err := fetch(t, "not-a-url"); err == nil {
		t.Error("relative URL should be rejected")
	}
}
