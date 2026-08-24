package source

import (
	"regexp"
	"strings"
)

var (
	scriptRe   = regexp.MustCompile(`(?si)<script[^>]*>.*?</script\s*>`)
	svgRe      = regexp.MustCompile(`(?si)<svg[^>]*>.*?</svg\s*>`)
	styleRe    = regexp.MustCompile(`(?si)<style[^>]*>.*?</style\s*>`)
	tableRe    = regexp.MustCompile(`(?si)<table[^>]*>.*?</table\s*>`)
	tagRe      = regexp.MustCompile(`<[^>]*>`)
	wsRe       = regexp.MustCompile(`[ \t]+`)
	trailingRe = regexp.MustCompile(`[ \t]+\n`)
	nlRe       = regexp.MustCompile(`\n{3,}`)
)

func stripHTMLContent(s string) string {
	if s == "" {
		return s
	}
	s = scriptRe.ReplaceAllString(s, "")
	s = svgRe.ReplaceAllString(s, "")
	s = styleRe.ReplaceAllString(s, "")
	s = tableRe.ReplaceAllString(s, "")
	s = tagRe.ReplaceAllString(s, " ")
	s = wsRe.ReplaceAllLiteralString(s, " ")
	s = trailingRe.ReplaceAllLiteralString(s, "\n")
	s = nlRe.ReplaceAllLiteralString(s, "\n\n")
	return strings.TrimSpace(s)
}
