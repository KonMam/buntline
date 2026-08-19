// Skill loading for the commands module: the open Agent Skills format
// (agentskills.io), the portable subset that every harness reads.
//
// A skill is a directory with a SKILL.md entrypoint:
//
//	skills/<name>/SKILL.md
//
// The file has YAML frontmatter between --- markers and a markdown body.
// The description (from frontmatter, or the first heading line) is what
// the composer menu and the model see; the body is the prompt, loaded
// only when the skill is invoked. This is progressive disclosure: a
// 300-line skill costs its description until it is used.
//
// Only the portable frontmatter subset is parsed here (the fields that
// survive outside Claude Code): name, description, allowed-tools,
// metadata, license, compatibility. Extension fields are ignored rather
// than rejected, so skills written for other harnesses load unchanged.
package commands

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is one SKILL.md entry. AllowedTools is the turn-scoped approval
// grant: the listed tools run without the approval gate during the turn
// that invokes the skill, and the grant clears on the next user message.
type Skill struct {
	Name         string
	Description  string
	Body         string
	AllowedTools []string
	// Source is "project" or "user"; used to resolve collisions.
	Source string
}

// skillDirs returns the directories scanned for skills, in precedence
// order (project first, user-global second; a project skill overrides a
// user skill with the same name).
func skillDirs(workdir, userConfigDir string) []string {
	dirs := []string{filepath.Join(workdir, ".buntline", "skills")}
	if userConfigDir != "" {
		dirs = append(dirs, filepath.Join(userConfigDir, "skills"))
	}
	return dirs
}

// loadSkills scans skillDirs for skills/*/SKILL.md. Duplicate names
// resolve by source precedence: project beats user. Within one source,
// the first directory wins.
func loadSkills(workdir, userConfigDir string) []Skill {
	var skills []Skill
	seen := map[string]bool{}
	for _, dir := range skillDirs(workdir, userConfigDir) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if seen[e.Name()] {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			skill := parseSkill(e.Name(), string(data), sourceOf(dir, userConfigDir))
			if skill.Name == "" {
				continue
			}
			skills = append(skills, skill)
			seen[e.Name()] = true
		}
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills
}

// sourceOf labels a skills directory as project or user.
func sourceOf(dir, userConfigDir string) string {
	if userConfigDir != "" && dir == filepath.Join(userConfigDir, "skills") {
		return "user"
	}
	return "project"
}

// parseSkill splits a SKILL.md into frontmatter and body. The frontmatter
// is the block between the opening --- and the closing --- at the very
// top; anything else is the body. A file without frontmatter uses the
// first heading line as the description, matching the commands behavior.
func parseSkill(dirName, content, source string) Skill {
	body := content
	fm := map[string]string{}
	if strings.HasPrefix(content, "---") {
		if rest, ok := cutFrontmatter(content); ok {
			body = rest
			fm = parseFrontmatter(frontmatterBlock(content))
		}
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return Skill{}
	}
	name := fm["name"]
	if name == "" {
		name = dirName
	}
	desc := fm["description"]
	if desc == "" {
		desc = firstHeading(body)
	}
	var allowed []string
	if raw := fm["allowed-tools"]; raw != "" {
		allowed = splitFields(raw)
	}
	return Skill{
		Name:         name,
		Description:  strings.TrimSpace(desc),
		Body:         body,
		AllowedTools: allowed,
		Source:       source,
	}
}

// cutFrontmatter returns the body after a leading --- frontmatter block,
// and whether a block was found.
func cutFrontmatter(content string) (string, bool) {
	rest := content[len("---"):]
	// The closing marker is a line that is exactly --- (or --- with
	// trailing whitespace).
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", false
	}
	return rest[idx+len("\n---"):], true
}

// frontmatterBlock returns the raw frontmatter (between the markers).
func frontmatterBlock(content string) string {
	rest := content[len("---"):]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return ""
	}
	return rest[:idx]
}

// parseFrontmatter parses a minimal YAML subset: `key: value` lines,
// values optionally quoted. Lists (`- item`) and nested maps are not
// needed for the portable subset; a list value for allowed-tools is
// joined into one field. Keys are lowercased.
func parseFrontmatter(block string) map[string]string {
	out := map[string]string{}
	var current string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if current != "" {
				out[current] = strings.TrimSpace(out[current] + " " + strings.TrimPrefix(line, "- "))
			}
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		val := strings.TrimSpace(line[colon+1:])
		val = unquote(val)
		if key != "" {
			out[key] = val
			current = key
		}
	}
	return out
}

// unquote strips a single matching pair of surrounding quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// splitFields splits an allowed-tools value on commas or whitespace,
// honoring quoted segments and parentheses (Bash(git add *) is one field).
func splitFields(s string) []string {
	fields := []string{}
	var cur strings.Builder
	quoted := false
	depth := 0
	for _, r := range s {
		switch {
		case r == '"' || r == '\'':
			quoted = !quoted
			cur.WriteRune(r)
		case r == '(' && !quoted:
			depth++
			cur.WriteRune(r)
		case r == ')' && !quoted:
			if depth > 0 {
				depth--
			}
			cur.WriteRune(r)
		case (r == ',' || r == ' ') && !quoted && depth == 0:
			if cur.Len() > 0 {
				fields = append(fields, unquote(strings.TrimSpace(cur.String())))
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		fields = append(fields, unquote(strings.TrimSpace(cur.String())))
	}
	return fields
}

// firstHeading returns the first non-empty line stripped of leading
// heading markers, the commands-module description convention.
func firstHeading(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.TrimSpace(strings.TrimLeft(line, "# "))
	}
	return ""
}

// Render substitutes skill/command arguments into a body:
//
//   - $ARGUMENTS      the full argument string
//   - $ARGUMENTS[N]   the Nth argument (0-based)
//   - $N              shorthand for $ARGUMENTS[N]
//
// An indexed placeholder with no corresponding argument stays literal
// (Claude Code's behavior). A backslash before $ escapes it: \$ARGUMENTS
// and \$0 stay as literal text. When the body has no $ARGUMENTS
// placeholder and arguments were passed, the arguments are appended as
// "ARGUMENTS: <value>" (Claude Code's fallback).
func Render(body, args string) string {
	args = strings.TrimSpace(args)
	if !strings.Contains(body, "$") {
		if args == "" {
			return body
		}
		return strings.TrimSpace(body) + "\n\nARGUMENTS: " + args
	}
	parts := splitArgs(args)
	var out strings.Builder
	i := 0
	for i < len(body) {
		ch := body[i]
		if ch == '\\' && i+1 < len(body) && body[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		if ch == '$' {
			if tok, n := matchArgToken(body[i:]); tok != "" {
				out.WriteString(substitute(tok, args, parts))
				i += n
				continue
			}
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// matchArgToken matches a $ token at the start of s and returns it plus
// its length. Valid tokens: $ARGUMENTS, $ARGUMENTS[N], $N.
func matchArgToken(s string) (string, int) {
	if strings.HasPrefix(s, "$ARGUMENTS") {
		rest := s[len("$ARGUMENTS"):]
		if len(rest) > 0 && rest[0] == '[' {
			end := strings.IndexByte(rest, ']')
			if end > 0 {
				return "$ARGUMENTS[" + rest[1:end] + "]", len("$ARGUMENTS") + end + 1
			}
			return "", 0
		}
		return "$ARGUMENTS", len("$ARGUMENTS")
	}
	if len(s) > 1 && s[1] >= '0' && s[1] <= '9' {
		n := 2
		for n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		return s[:n], n
	}
	return "", 0
}

// substitute resolves a matched token against the argument string.
func substitute(tok, args string, parts []string) string {
	if tok == "$ARGUMENTS" {
		return args
	}
	// Parse the index out of $ARGUMENTS[N] or $N. The token was matched
	// by matchArgToken, so the index is a valid digit run.
	var idx int
	digitStart := 0
	if strings.HasPrefix(tok, "$ARGUMENTS[") {
		digitStart = len("$ARGUMENTS[")
	} else {
		digitStart = 1
	}
	digits := tok[digitStart : len(tok)-1] // strip trailing ] for $ARGUMENTS[N]
	if digitStart == 1 {
		digits = tok[1:]
	}
	for _, r := range digits {
		idx = idx*10 + int(r-'0')
	}
	if idx < 0 || idx >= len(parts) {
		return tok // stays literal
	}
	return parts[idx]
}

// splitArgs splits an argument string on whitespace, honoring quotes so
// "hello world" stays one argument. A single backslash before a quote
// keeps it literal (the composer's shell-style quoting).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := rune(0)
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quoted != 0:
			cur.WriteRune(r)
		case r == '\\':
			escaped = true
		case quoted != 0 && r == quoted:
			quoted = 0
		case quoted == 0 && (r == '"' || r == '\''):
			quoted = r
		case quoted == 0 && (r == ' ' || r == '\t' || r == '\n'):
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
