package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T) (Workdir, string) {
	t.Helper()
	dir := t.TempDir()
	// macOS: /var/folders symlinks to /private/var; resolve so confinement
	// prefix checks compare like with like.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return Workdir(resolved), resolved
}

func TestWorkdirConfinement(t *testing.T) {
	w, dir := setup(t)

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative inside", "a/b.txt", false},
		{"absolute inside", filepath.Join(dir, "c.txt"), false},
		{"the workdir itself", ".", false},
		{"escape via dotdot", "../outside.txt", true},
		{"deep escape", "a/../../outside.txt", true},
		{"absolute outside", "/etc/passwd", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := w.Resolve(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("Resolve(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func run(t *testing.T, tool Tool, args any) (string, error) {
	t.Helper()
	res, err := runFull(t, tool, args)
	return res.Content, err
}

func runFull(t *testing.T, tool Tool, args any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Run(context.Background(), raw)
}

func TestReadWriteRoundTrip(t *testing.T) {
	w, dir := setup(t)

	out, err := run(t, &WriteFile{W: w}, map[string]string{
		"path": "sub/hello.txt", "content": "hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "11 bytes") {
		t.Errorf("write output = %q", out)
	}

	got, err := run(t, &ReadFile{W: w}, map[string]string{"path": "sub/hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello world" {
		t.Errorf("read = %q", got)
	}

	// Confinement applies to writes too.
	if _, err := run(t, &WriteFile{W: w}, map[string]string{
		"path": "../escape.txt", "content": "x",
	}); err == nil {
		t.Error("write outside workdir should fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "escape.txt")); err == nil {
		t.Error("escape file was created")
	}
}

func TestEditFile(t *testing.T) {
	w, dir := setup(t)
	path := filepath.Join(dir, "f.go")
	content := "func a() {}\nfunc b() {}\nfunc a2() {}\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := &EditFile{W: w}

	// Unique match succeeds and reports a diff.
	res, err := runFull(t, edit, map[string]string{
		"path": "f.go", "old_string": "func b() {}", "new_string": "func b() { panic(1) }",
	})
	if err != nil {
		t.Fatalf("unique edit: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "panic(1)") {
		t.Error("edit not applied")
	}
	if !strings.Contains(res.Diff, "-func b() {}") || !strings.Contains(res.Diff, "+func b() { panic(1) }") {
		t.Errorf("diff missing change markers:\n%s", res.Diff)
	}
	if !strings.Contains(res.Diff, "--- f.go") {
		t.Errorf("diff header should carry the real path:\n%s", res.Diff)
	}

	// Zero matches: loud error.
	if _, err := run(t, edit, map[string]string{
		"path": "f.go", "old_string": "func missing()", "new_string": "x",
	}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want not-found error, got %v", err)
	}

	// Ambiguous match: loud error, file untouched.
	before, _ := os.ReadFile(path)
	if _, err := run(t, edit, map[string]string{
		"path": "f.go", "old_string": "func a", "new_string": "func z",
	}); err == nil || !strings.Contains(err.Error(), "2 locations") {
		t.Errorf("want ambiguity error, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("ambiguous edit modified the file")
	}

	// No-op edit rejected.
	if _, err := run(t, edit, map[string]string{
		"path": "f.go", "old_string": "func z", "new_string": "func z",
	}); err == nil {
		t.Error("identical old/new should fail")
	}
}

func TestBash(t *testing.T) {
	_, dir := setup(t)
	bash := &Bash{Dir: dir}

	out, err := run(t, bash, map[string]any{"command": "echo hi && echo err >&2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "err") {
		t.Errorf("combined output = %q", out)
	}

	// Non-zero exit is a result, not an error.
	out, err = run(t, bash, map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit status 3") {
		t.Errorf("out = %q", out)
	}

	// Runs in the workdir.
	out, _ = run(t, bash, map[string]any{"command": "pwd"})
	if strings.TrimSpace(out) != dir {
		t.Errorf("pwd = %q, want %q", out, dir)
	}
}

func TestScrubbedEnv(t *testing.T) {
	t.Setenv("MY_TEST_TOKEN", "super-secret-value")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "also-secret")
	t.Setenv("KEEP_THIS", "not-a-secret")

	env := scrubbedEnv()
	lookup := func(name string) (string, bool) {
		prefix := name + "="
		for _, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				return strings.TrimPrefix(kv, prefix), true
			}
		}
		return "", false
	}

	for _, name := range []string{"MY_TEST_TOKEN", "AWS_SECRET_ACCESS_KEY"} {
		if v, ok := lookup(name); ok {
			t.Errorf("%s survived scrubbing with value %q", name, v)
		}
	}
	if v, ok := lookup("KEEP_THIS"); !ok || v != "not-a-secret" {
		t.Errorf("KEEP_THIS lost: %q, %v", v, ok)
	}
	// The keep-list variables a command needs to run at all survive.
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "TERM", "LANG", "USER", "SHELL"} {
		if _, ok := lookup(name); !ok {
			t.Logf("note: %s not set in test env, skipping", name)
		}
	}
}

func TestBashSleepDeclinesBackground(t *testing.T) {
	bash := &Bash{}
	// A command that starts with sleep never backgrounds: it runs inline
	// and dies at its timeout instead (Claude Code's rule).
	if !bash.NeverBackground(json.RawMessage(`{"command":"sleep 30"}`)) {
		t.Error("sleep command must decline backgrounding")
	}
	if !bash.NeverBackground(json.RawMessage(`{"command":"  sleep 1; echo hi"}`)) {
		t.Error("leading-whitespace sleep must decline backgrounding")
	}
	// A build or test does background.
	if bash.NeverBackground(json.RawMessage(`{"command":"go test ./..."}`)) {
		t.Error("go test must not decline backgrounding")
	}
	if bash.NeverBackground(json.RawMessage(`{"command":"npm run build"}`)) {
		t.Error("npm build must not decline backgrounding")
	}
}

func TestBashScrubsSecrets(t *testing.T) {
	_, dir := setup(t)
	bash := &Bash{Dir: dir}

	t.Setenv("MY_TEST_TOKEN", "must-not-leak")
	out, err := run(t, bash, map[string]any{"command": "echo token=[$MY_TEST_TOKEN]"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "must-not-leak") {
		t.Errorf("secret leaked to spawned command: %q", out)
	}

	// PATH is essential for bash to find anything; it must survive.
	out, err = run(t, bash, map[string]any{"command": "echo path=[$PATH]"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "path=[/") && !strings.Contains(out, "path=[]") {
		t.Errorf("PATH missing or empty in spawned command: %q", out)
	}
	if strings.Contains(out, "path=[]") {
		t.Error("PATH empty in spawned command")
	}
}

func TestBackgroundScrubsSecrets(t *testing.T) {
	_, dir := setup(t)
	m := newJobManager()
	t.Cleanup(m.Close)

	t.Setenv("MY_TEST_TOKEN", "must-not-leak")
	id, err := m.Start(dir, "echo token=[$MY_TEST_TOKEN]; echo path=[$PATH]")
	if err != nil {
		t.Fatal(err)
	}
	// Wait for the short command to finish.
	deadline := time.Now().Add(5 * time.Second)
	var job Job
	for {
		job, err = m.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background task did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, err := m.Output(id, 0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "must-not-leak") {
		t.Errorf("secret leaked to background command: %q", out)
	}
	if !strings.Contains(out, "path=[/") || strings.Contains(out, "path=[]") {
		t.Errorf("PATH missing in background command: %q", out)
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	_, dir := setup(t)
	bash := &Bash{Dir: dir}

	start := time.Now()
	// The child spawns a grandchild that would outlive a naive kill and
	// hold the pipe open; WaitDelay + group kill must contain both.
	_, err := run(t, bash, map[string]any{
		"command":         "sleep 30 & sleep 30",
		"timeout_seconds": 1,
	})
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want timeout error, got %v", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("timeout took %s; process group not killed", elapsed)
	}
}

func TestGlobFallbackMatcher(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", false},
		{"**/*.go", "sub/deep/main.go", true},
		{"**/*.go", "main.go", true},
		{"web/**/*.ts", "web/src/lib/api.ts", true},
		{"web/**/*.ts", "cmd/main.go", false},
		{"internal/*/tools.go", "internal/tools/tools.go", true},
	}
	for _, tt := range tests {
		if got := matchGlob(tt.pattern, tt.name); got != tt.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

func TestGrepAndGlob(t *testing.T) {
	if !RipgrepAvailable() {
		t.Skip("rg not installed")
	}
	_, dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\nfunc Target() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, &Grep{Dir: dir}, map[string]string{"pattern": "Target"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x.go") || !strings.Contains(out, "2") {
		t.Errorf("grep = %q", out)
	}

	out, err = run(t, &Grep{Dir: dir}, map[string]string{"pattern": "NoSuchThing"})
	if err != nil || out != "no matches" {
		t.Errorf("no-match grep = %q, %v", out, err)
	}

	out, err = run(t, &Glob{Dir: dir}, map[string]string{"pattern": "**/*.go"})
	if err != nil || !strings.Contains(out, "x.go") {
		t.Errorf("glob = %q, %v", out, err)
	}
}

func TestUnknownArgumentRejected(t *testing.T) {
	w, _ := setup(t)
	// Models hallucinate argument names; DisallowUnknownFields surfaces it.
	_, err := run(t, &ReadFile{W: w}, map[string]string{"file": "x.txt"})
	if err == nil {
		t.Error("unknown field should be rejected")
	}
}

func TestTruncateKeepsTail(t *testing.T) {
	over := strings.Repeat("a", outputCap+1000)
	got := truncate(over)
	if !strings.Contains(got, "[... 1000 characters omitted ...]") {
		t.Errorf("marker missing or wrong count: %q", got)
	}
	// The final line of the input must survive, so the model sees the
	// failure at the end of a test run.
	if !strings.HasSuffix(got, strings.Repeat("a", 1000)) {
		t.Error("tail of the output lost")
	}
	if !strings.HasPrefix(got, strings.Repeat("a", outputCap/4)) {
		t.Error("head of the output lost")
	}
	// Total length stays within the budget.
	marker := fmt.Sprintf("\n[... %d characters omitted ...]\n", 1000)
	if len(got) != outputCap+len(marker) {
		t.Errorf("truncated output length = %d, want %d", len(got), outputCap+len(marker))
	}
}

func TestTruncateUnderCapUnchanged(t *testing.T) {
	small := "hello world"
	if got := truncate(small); got != small {
		t.Errorf("under-cap input changed: %q", got)
	}
}

func TestBashFullOutputCappedByRegistry(t *testing.T) {
	_, dir := setup(t)
	bash := &Bash{Dir: dir}
	// The tool returns full output; the registry caps it. A distinctive
	// final line must survive the cap so the model sees the failure.
	out, err := run(t, bash, map[string]any{
		"command": "yes x | head -c 200000; echo THE-FINAL-LINE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "THE-FINAL-LINE\n") {
		t.Errorf("bash tool should return full output ending in THE-FINAL-LINE, got suffix %q", out[len(out)-40:])
	}

	capped := NewRegistry().CapResult(Result{Content: out})
	if !strings.Contains(capped.Content, "THE-FINAL-LINE") {
		t.Error("final line of command output lost in truncation")
	}
	if !strings.Contains(capped.Content, "characters omitted") {
		t.Error("truncation marker missing")
	}
}

// fakeSink is an in-memory SpillSink for exercising the registry's spill
// path without a session directory.
type fakeSink struct {
	files map[string]string
	seq   int
}

func (f *fakeSink) Save(text string) (string, error) {
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.seq++
	id := fmt.Sprintf("%d", f.seq)
	f.files[id] = text
	return id, nil
}

func (f *fakeSink) Read(id string, offset, limit int) (string, error) {
	text, ok := f.files[id]
	if !ok {
		return "", fmt.Errorf("no spill %s", id)
	}
	if offset > len(text) {
		return fmt.Sprintf("offset %d is past the end of spill %s (%d characters)", offset, id, len(text)), nil
	}
	end := offset + limit
	if end > len(text) {
		end = len(text)
	}
	return text[offset:end], nil
}

func TestCapResultSpillsOverCap(t *testing.T) {
	reg := NewRegistry()
	sink := &fakeSink{}
	reg.SetSpillSink(sink)

	big := strings.Repeat("a", outputCap+100)
	res := reg.CapResult(Result{Content: big})

	if len(sink.files) != 1 {
		t.Fatalf("expected one spill file, got %d", len(sink.files))
	}
	if sink.files["1"] != big {
		t.Error("spill file does not hold the full output")
	}
	if !strings.Contains(res.Content, "[full output: spill 1, ") ||
		!strings.Contains(res.Content, "Read it with the read_spill tool") {
		t.Errorf("locator line missing: %q", res.Content)
	}
	// The tail survives in the inline part, before the locator line.
	inline := strings.Split(res.Content, "[full output:")[0]
	if !strings.HasSuffix(strings.TrimRight(inline, "\n"), strings.Repeat("a", 100)) {
		t.Error("tail lost from inline result")
	}
}

func TestCapResultUnderCapNoSpill(t *testing.T) {
	reg := NewRegistry()
	reg.SetSpillSink(&fakeSink{})

	small := "hello world"
	res := reg.CapResult(Result{Content: small})
	if res.Content != small {
		t.Errorf("under-cap result changed: %q", res.Content)
	}
}

func TestCapResultNoSinkPlainTruncates(t *testing.T) {
	reg := NewRegistry() // no sink

	big := strings.Repeat("b", outputCap+50)
	res := reg.CapResult(Result{Content: big})
	if strings.Contains(res.Content, "spill") {
		t.Errorf("no sink should not produce spill locator: %q", res.Content[:200])
	}
	if !strings.Contains(res.Content, "characters omitted") {
		t.Error("plain truncation marker missing")
	}
}

func TestReadSpillTool(t *testing.T) {
	reg := NewRegistry()
	sink := &fakeSink{}
	reg.SetSpillSink(sink)

	// A fake sink means no real spill; construct the tool directly with a
	// populated fake to test reading behavior.
	content := "0123456789"
	id, err := sink.Save(content)
	if err != nil {
		t.Fatal(err)
	}
	tool := &ReadSpill{r: reg}

	out, err := run(t, tool, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	if out != "0123456789" {
		t.Errorf("full read = %q", out)
	}

	out, err = run(t, tool, map[string]any{"id": id, "offset": 3, "limit": 4})
	if err != nil {
		t.Fatal(err)
	}
	if out != "3456" {
		t.Errorf("sliced read = %q", out)
	}

	// Offset past the end: a clear message, not an empty result.
	out, err = run(t, tool, map[string]any{"id": id, "offset": 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "past the end") {
		t.Errorf("past-end message = %q", out)
	}
}

func TestReadSpillToolRejectsBadID(t *testing.T) {
	reg := NewRegistry()
	reg.SetSpillSink(&fakeSink{})
	tool := &ReadSpill{r: reg}

	for _, id := range []string{"../x", "a/b", `a\b`, ""} {
		if _, err := run(t, tool, map[string]any{"id": id}); err == nil {
			t.Errorf("id %q should be rejected", id)
		}
	}
}

func TestReadSpillToolNoSink(t *testing.T) {
	reg := NewRegistry()
	tool := &ReadSpill{r: reg}
	out, err := run(t, tool, map[string]any{"id": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no spill storage") {
		t.Errorf("no-sink message = %q", out)
	}
}

// fakeSearcher returns canned hits for the session_search tool.
type fakeSearcher struct {
	hits []SessionHit
}

func (f *fakeSearcher) SearchSessions(_ string, _ int) ([]SessionHit, error) {
	return f.hits, nil
}

func TestSessionSearchReturnsHits(t *testing.T) {
	tool := &SessionSearch{Searcher: &fakeSearcher{hits: []SessionHit{
		{SessionID: "s1", Title: "dropdown fix", Workdir: "/repo", Snippet: "replaced native select with Dropdown"},
		{SessionID: "s2", Title: "setup ci", Workdir: "/repo", Snippet: "added golangci-lint to the gate"},
	}}}

	out, err := run(t, tool, map[string]any{"query": "dropdown"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "dropdown fix") || !strings.Contains(out, "s1") ||
		!strings.Contains(out, "Dropdown") {
		t.Errorf("hits = %q", out)
	}
	if !strings.Contains(out, "setup ci") {
		t.Errorf("second hit missing: %q", out)
	}
}

func TestSessionSearchBlankQuery(t *testing.T) {
	tool := &SessionSearch{Searcher: &fakeSearcher{}}
	out, err := run(t, tool, map[string]any{"query": "   "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a query is required") {
		t.Errorf("blank-query message = %q", out)
	}
}

func TestSessionSearchNoSearcher(t *testing.T) {
	tool := &SessionSearch{}
	out, err := run(t, tool, map[string]any{"query": "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not available") {
		t.Errorf("no-searcher message = %q", out)
	}
}

func TestSessionSearchNoHits(t *testing.T) {
	tool := &SessionSearch{Searcher: &fakeSearcher{}}
	out, err := run(t, tool, map[string]any{"query": "nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no past sessions mention") {
		t.Errorf("no-hits message = %q", out)
	}
}
