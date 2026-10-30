// Package filter applies an rclone filter file (the `--filter-from` syntax) to
// individual paths. rclone refuses `--files-from` together with any filter, so
// when FoxCloud uploads a handful of changed files by name it has to apply the
// job's filter file itself, with the same rules rclone would.
package filter

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type rule struct {
	include bool
	dirOnly bool // the pattern ended in "/": it names a directory (and so everything in it)
	re      *regexp.Regexp
}

// Matcher is a parsed filter file. The zero value (and nil) include everything.
type Matcher struct {
	rules []rule
}

// Load reads a filter file. An empty path gives a matcher that includes
// everything.
func Load(path string) (*Matcher, error) {
	if path == "" {
		return &Matcher{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m Matcher
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if len(line) < 3 || (line[0] != '+' && line[0] != '-') || line[1] != ' ' {
			return nil, fmt.Errorf("%s:%d: a filter rule starts with \"+ \" or \"- \"", path, n)
		}
		r, err := parseRule(line[0] == '+', strings.TrimSpace(line[2:]))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		m.rules = append(m.rules, r)
	}
	return &m, sc.Err()
}

func parseRule(include bool, pattern string) (rule, error) {
	r := rule{include: include}
	if strings.HasSuffix(pattern, "/") {
		r.dirOnly = true
		pattern = strings.TrimSuffix(pattern, "/")
	}
	re, err := compile(pattern)
	if err != nil {
		return r, err
	}
	r.re = re
	return r, nil
}

// compile turns an rclone glob into a regexp over slash-separated paths with
// no leading slash. A pattern starting with "/" is anchored at the root;
// otherwise it may match at any depth.
func compile(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	if strings.HasPrefix(pattern, "/") {
		pattern = pattern[1:]
		b.WriteString("^")
	} else {
		b.WriteString("(?:^|.*/)")
	}
	inClass, braces := false, 0
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case inClass:
			switch c {
			case ']':
				inClass = false
				b.WriteByte(']')
			case '\\':
				b.WriteString(`\\`)
			default:
				b.WriteByte(c)
			}
		case c == '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			inClass = true
			b.WriteByte('[')
			if i+1 < len(pattern) && pattern[i+1] == '!' {
				b.WriteByte('^')
				i++
			}
		case c == '{':
			braces++
			b.WriteString("(?:")
		case c == '}' && braces > 0:
			braces--
			b.WriteByte(')')
		case c == ',' && braces > 0:
			b.WriteByte('|')
		case c == '\\' && i+1 < len(pattern):
			i++
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	if inClass || braces != 0 {
		return nil, fmt.Errorf("unbalanced bracket or brace in %q", pattern)
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// decide runs the rules over one path in order; the first match wins. ok is
// false when no rule matched.
func (m *Matcher) decide(path string, isDir bool) (include, ok bool) {
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(path) {
			return r.include, true
		}
	}
	return true, false
}

// Include reports whether a file (path relative to the sync root, with forward
// slashes) is synced. A directory rule excludes everything below it, so every
// parent directory is checked before the file itself, as rclone does when it
// walks the tree.
func (m *Matcher) Include(path string) bool {
	if m == nil || len(m.rules) == 0 {
		return true
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		if inc, ok := m.decide(strings.Join(parts[:i], "/"), true); ok && !inc {
			return false
		}
	}
	inc, _ := m.decide(path, false)
	return inc
}

// IncludeDir is Include for a directory.
func (m *Matcher) IncludeDir(path string) bool {
	if m == nil || len(m.rules) == 0 {
		return true
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	for i := 1; i <= len(parts); i++ {
		if inc, ok := m.decide(strings.Join(parts[:i], "/"), true); ok && !inc {
			return false
		}
	}
	return true
}
