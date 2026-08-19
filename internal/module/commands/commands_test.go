package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates .buntline/skills/<name>/SKILL.md in the workdir (or
// skills/<name>/SKILL.md under a user config dir) with the given content.
func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeProjectSkill writes .buntline/skills/<name>/SKILL.md under a workdir.
func writeProjectSkill(t *testing.T, workdir, name, content string) {
	t.Helper()
	writeSkill(t, filepath.Join(workdir, ".buntline"), name, content)
}

func TestParseSkillFrontmatter(t *testing.T) {
	content := `---
name: code-review
description: Reviews the current diff for risks.
allowed-tools: Bash(git diff *) Read Grep
license: MIT
---

## Instructions
Review the changes.
`
	s := parseSkill("code-review", content, "project")
	if s.Name != "code-review" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Description != "Reviews the current diff for risks." {
		t.Errorf("description = %q", s.Description)
	}
	if !stringsContains(s.AllowedTools, "Bash(git diff *)") {
		t.Errorf("allowed-tools = %v", s.AllowedTools)
	}
	if !strings.Contains(s.Body, "## Instructions") {
		t.Errorf("body = %q", s.Body)
	}
	if !strings.Contains(s.Body, "Review the changes.") {
		t.Errorf("body lost tail: %q", s.Body)
	}
}

func TestParseSkillNoFrontmatterUsesFirstHeading(t *testing.T) {
	content := "# Deploy\nDeploy the app to production.\n"
	s := parseSkill("deploy", content, "project")
	if s.Name != "deploy" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Description != "Deploy" {
		t.Errorf("description = %q", s.Description)
	}
	if len(s.AllowedTools) != 0 {
		t.Errorf("allowed-tools should be empty, got %v", s.AllowedTools)
	}
}

func TestParseSkillFrontmatterDescriptionOnly(t *testing.T) {
	content := `---
description: Summarizes uncommitted changes.
---

Summarize the diff.
`
	s := parseSkill("summarize", content, "project")
	if s.Name != "summarize" {
		t.Errorf("name = %q (want dir name)", s.Name)
	}
	if s.Description != "Summarizes uncommitted changes." {
		t.Errorf("description = %q", s.Description)
	}
}

func TestLoadSkillsProjectAndUser(t *testing.T) {
	workdir := t.TempDir()
	userDir := t.TempDir()
	writeProjectSkill(t, workdir, "review", "---\ndescription: Project review.\n---\n\nBody.")
	writeSkill(t, userDir, "global", "---\ndescription: Global skill.\n---\n\nGlobal body.")
	// Same name in user and project: project wins.
	writeSkill(t, userDir, "review", "---\ndescription: User review.\n---\n\nUser body.")

	skills := loadSkills(workdir, userDir)
	if len(skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(skills))
	}
	byName := map[string]Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}
	if s := byName["review"]; s.Description != "Project review." || s.Source != "project" {
		t.Errorf("review skill = %+v, want project source", s)
	}
	if s := byName["global"]; s.Description != "Global skill." || s.Source != "user" {
		t.Errorf("global skill = %+v, want user source", s)
	}
}

func TestHandleListIncludesCommandsAndSkills(t *testing.T) {
	workdir := t.TempDir()
	// A plain command.
	cmdDir := filepath.Join(workdir, ".buntline", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "deploy.md"), []byte("# Deploy\nRun the deploy."), 0o644); err != nil {
		t.Fatal(err)
	}
	// A skill with the same name overrides the command.
	writeProjectSkill(t, workdir, "deploy", "---\ndescription: Skill deploy.\n---\n\nSkill body.")

	m := &Module{Lookup: func(string) (string, error) { return workdir, nil }, UserConfigDir: func() string { return t.TempDir() }}
	req := httptest.NewRequest("GET", "/list?session=s1", nil)
	rec := httptest.NewRecorder()
	m.handleList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Commands []command `json:"commands"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("got %d commands, want 1 (skill overrides command): %+v", len(resp.Commands), resp.Commands)
	}
	c := resp.Commands[0]
	if c.Name != "deploy" || !c.Skill || c.Description != "Skill deploy." {
		t.Errorf("command = %+v, want skill deploy", c)
	}
	if len(c.AllowedTools) != 0 {
		t.Errorf("allowed-tools = %v, want none", c.AllowedTools)
	}
}

func TestHandleListSkillAllowedTools(t *testing.T) {
	workdir := t.TempDir()
	writeProjectSkill(t, workdir, "commit", "---\ndescription: Commit changes.\nallowed-tools: Bash(git add *) Bash(git commit *)\n---\n\nCommit.")
	m := &Module{Lookup: func(string) (string, error) { return workdir, nil }, UserConfigDir: func() string { return t.TempDir() }}
	req := httptest.NewRequest("GET", "/list?session=s1", nil)
	rec := httptest.NewRecorder()
	m.handleList(rec, req)
	var resp struct {
		Commands []command `json:"commands"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Commands) != 1 {
		t.Fatalf("got %d commands", len(resp.Commands))
	}
	c := resp.Commands[0]
	if len(c.AllowedTools) != 2 {
		t.Fatalf("allowed-tools = %v, want 2", c.AllowedTools)
	}
	if c.AllowedTools[0] != "Bash(git add *)" || c.AllowedTools[1] != "Bash(git commit *)" {
		t.Errorf("allowed-tools = %v", c.AllowedTools)
	}
}

func TestHandleListNoSkillsEmpty(t *testing.T) {
	workdir := t.TempDir()
	m := &Module{Lookup: func(string) (string, error) { return workdir, nil }, UserConfigDir: func() string { return t.TempDir() }}
	req := httptest.NewRequest("GET", "/list?session=s1", nil)
	rec := httptest.NewRecorder()
	m.handleList(rec, req)
	var resp struct {
		Commands []command `json:"commands"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Commands) != 0 {
		t.Fatalf("got %d commands, want 0", len(resp.Commands))
	}
}

func stringsContains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func TestRenderArguments(t *testing.T) {
	cases := []struct {
		name, body, args, want string
	}{
		{"full args", "Fix $ARGUMENTS", "issue 123", "Fix issue 123"},
		{"positional", "Migrate $0 from $1 to $2", "SearchBar React Vue", "Migrate SearchBar from React to Vue"},
		{"indexed", "Use $ARGUMENTS[0] and $ARGUMENTS[1]", "a b c", "Use a and b"},
		{"missing index stays literal", "Step $5", "a", "Step $5"},
		{"no placeholder appends", "Do the thing", "with care", "Do the thing\n\nARGUMENTS: with care"},
		{"no placeholder no args unchanged", "Do the thing", "", "Do the thing"},
		{"escaped dollar", "Price \\$5", "", "Price $5"},
		{"escaped arguments", "Say \\$ARGUMENTS", "hi", "Say $ARGUMENTS"},
		{"quoted single arg", "Hello $0", `"hello world"`, "Hello hello world"},
		{"multiple placeholders", "$0 and $1", "x y", "x and y"},
		{"empty args with placeholder", "Fix $ARGUMENTS", "", "Fix "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Render(tc.body, tc.args)
			if got != tc.want {
				t.Errorf("Render(%q, %q) = %q, want %q", tc.body, tc.args, got, tc.want)
			}
		})
	}
}

func TestHandleRenderRoute(t *testing.T) {
	workdir := t.TempDir()
	writeProjectSkill(t, workdir, "migrate", "---\ndescription: Migrate a component.\n---\n\nMigrate the $0 component from $1 to $2.")
	m := &Module{Lookup: func(string) (string, error) { return workdir, nil }, UserConfigDir: func() string { return t.TempDir() }}

	body := strings.NewReader(`{"session":"s1","name":"migrate","args":"SearchBar React Vue"}`)
	req := httptest.NewRequest("POST", "/render", body)
	rec := httptest.NewRecorder()
	m.handleRender(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if want := "Migrate the SearchBar component from React to Vue."; resp.Body != want {
		t.Errorf("rendered = %q, want %q", resp.Body, want)
	}
}

func TestHandleRenderUnknown(t *testing.T) {
	workdir := t.TempDir()
	m := &Module{Lookup: func(string) (string, error) { return workdir, nil }, UserConfigDir: func() string { return t.TempDir() }}
	req := httptest.NewRequest("POST", "/render", strings.NewReader(`{"session":"s1","name":"nope","args":""}`))
	rec := httptest.NewRecorder()
	m.handleRender(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleRenderCommandBody(t *testing.T) {
	workdir := t.TempDir()
	cmdDir := filepath.Join(workdir, ".buntline", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "deploy.md"), []byte("Deploy $ARGUMENTS"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Module{Lookup: func(string) (string, error) { return workdir, nil }, UserConfigDir: func() string { return t.TempDir() }}
	req := httptest.NewRequest("POST", "/render", strings.NewReader(`{"session":"s1","name":"deploy","args":"to prod"}`))
	rec := httptest.NewRecorder()
	m.handleRender(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if want := "Deploy to prod"; resp.Body != want {
		t.Errorf("rendered = %q, want %q", resp.Body, want)
	}
}

func TestSkillToolRendersBody(t *testing.T) {
	workdir := t.TempDir()
	writeProjectSkill(t, workdir, "review", "---\ndescription: Review the diff.\n---\n\nReview the $0 changes carefully.")
	tool := &SkillTool{Workdir: workdir, UserConfigDir: t.TempDir()}
	args, _ := json.Marshal(map[string]string{"name": "review", "args": "auth"})
	res, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Review the auth changes carefully."; res.Content != want {
		t.Errorf("content = %q, want %q", res.Content, want)
	}
}

func TestSkillToolUnknownName(t *testing.T) {
	workdir := t.TempDir()
	tool := &SkillTool{Workdir: workdir, UserConfigDir: t.TempDir()}
	args, _ := json.Marshal(map[string]string{"name": "nope"})
	res, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "unknown skill \"nope\"") {
		t.Errorf("content = %q, want unknown-skill message", res.Content)
	}
	if !strings.Contains(res.Content, "none") {
		t.Errorf("content = %q, want 'none' when no skills", res.Content)
	}
}

func TestSkillToolDefListsSkills(t *testing.T) {
	workdir := t.TempDir()
	writeProjectSkill(t, workdir, "review", "---\ndescription: Review the diff.\n---\n\nBody.")
	writeProjectSkill(t, workdir, "deploy", "---\ndescription: Deploy to prod.\n---\n\nBody.")
	tool := &SkillTool{Workdir: workdir, UserConfigDir: t.TempDir()}
	def := tool.Def()
	if !strings.Contains(def.Description, "review") {
		t.Errorf("description = %q, want review listed", def.Description)
	}
	if !strings.Contains(def.Description, "deploy") {
		t.Errorf("description = %q, want deploy listed", def.Description)
	}
	if !strings.Contains(def.Description, "Review the diff.") {
		t.Errorf("description = %q, want review description", def.Description)
	}
}

func TestSkillToolEmptyName(t *testing.T) {
	tool := &SkillTool{Workdir: t.TempDir(), UserConfigDir: t.TempDir()}
	args, _ := json.Marshal(map[string]string{"name": ""})
	if _, err := tool.Run(context.Background(), args); err == nil {
		t.Fatal("want error for empty name")
	}
}
