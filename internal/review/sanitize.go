package review

import (
	"regexp"
	"strings"
)

const mentionZWSP = "\u200b"

var (
	mentionPattern       = regexp.MustCompile(`(^|[^A-Za-z0-9_])@([A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?)`)
	unsafeSuggestionHTML = regexp.MustCompile(`(?i)<\s*/?\s*(?:script|iframe|object|embed|link|meta|svg|img|style|video|form|textarea)\b|<!--|javascript:|\bon[a-z]+\s*=`)
	allowedHTMLTags      = map[string]struct{}{
		"details": {},
		"summary": {},
		"sub":     {},
	}
)

func sanitizeModelText(s string) string {
	if s == "" {
		return s
	}
	return neutralizeMentions(stripDisallowedHTML(s))
}

func suggestionLooksLikeHTML(s string) bool {
	return unsafeSuggestionHTML.MatchString(s)
}

func neutralizeMentions(s string) string {
	return mentionPattern.ReplaceAllString(s, "$1@"+mentionZWSP+"$2")
}

func stripDisallowedHTML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			b.WriteString(s[i:])
			break
		}
		lt += i
		b.WriteString(s[i:lt])
		rest := s[lt:]
		if strings.HasPrefix(rest, "<!--") {
			if end := strings.Index(rest[4:], "-->"); end >= 0 {
				i = lt + 4 + end + 3
				continue
			}
			// Unclosed comments must not swallow formatter HTML concatenated after this field.
			break
		}
		if len(rest) >= 2 && rest[1] == '!' {
			if end := strings.IndexByte(rest, '>'); end >= 0 {
				i = lt + end + 1
				continue
			}
			break
		}
		end, name, closing, ok := parseHTMLTag(rest)
		if !ok {
			b.WriteByte('<')
			i = lt + 1
			continue
		}
		lower := strings.ToLower(name)
		if _, allowed := allowedHTMLTags[lower]; allowed {
			if closing {
				b.WriteString("</")
				b.WriteString(lower)
				b.WriteByte('>')
			} else {
				b.WriteByte('<')
				b.WriteString(lower)
				b.WriteByte('>')
			}
		}
		i = lt + end
	}
	return b.String()
}

func parseHTMLTag(s string) (end int, name string, closing bool, ok bool) {
	if len(s) < 2 || s[0] != '<' {
		return 0, "", false, false
	}
	i := 1
	if s[i] == '/' {
		closing = true
		i++
	}
	if i >= len(s) || !isASCIILetter(s[i]) {
		return 0, "", false, false
	}
	start := i
	i++
	for i < len(s) && isTagNameChar(s[i]) {
		i++
	}
	name = s[start:i]
	inQuote := byte(0)
	for i < len(s) {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			i++
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
			i++
		case '>':
			return i + 1, name, closing, true
		default:
			i++
		}
	}
	return 0, "", false, false
}

func isASCIILetter(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

func isTagNameChar(c byte) bool {
	return isASCIILetter(c) || c >= '0' && c <= '9' || c == '-'
}
