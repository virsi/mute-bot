// Package normalize cleans raw Telegram post text and detects its language
// before downstream pipeline stages (dedup, classify, rank) consume it.
package normalize

import (
	"regexp"
	"strings"
)

// Pre-compiled regexps used by Clean. They are package-level so the cost of
// regex compilation is paid once.
var (
	reURL     = regexp.MustCompile(`https?://\S+`)
	reMention = regexp.MustCompile(`@\w+`)
	reHashtag = regexp.MustCompile(`#\w+`)
	reSpaces  = regexp.MustCompile(`\s+`)
	// reAdvert strips trailing advert blocks: a newline followed by a marker
	// line ("реклама", "advert", "спонсор", "promo", "sponsored") and the rest
	// of the message. Case-insensitive, multi-line.
	reAdvert = regexp.MustCompile(`(?is)\n+\s*(реклама|advert|спонсор|promo|sponsored).*$`)
)

// Clean strips emojis, URLs, @mentions, #hashtags and trailing advert blocks
// from s, collapses all whitespace runs to single spaces, and trims the result.
func Clean(s string) string {
	s = reAdvert.ReplaceAllString(s, "")
	s = stripEmoji(s)
	s = reURL.ReplaceAllString(s, "")
	s = reMention.ReplaceAllString(s, "")
	s = reHashtag.ReplaceAllString(s, "")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// stripEmoji drops any rune that falls inside the common Unicode emoji blocks.
// Pictographs and dingbats are removed; ordinary punctuation is preserved.
func stripEmoji(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isEmoji(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isEmoji reports whether r is in one of the common emoji / symbol blocks.
// Conservative — only the well-known ranges; safe for RU/EN news text.
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F600 && r <= 0x1F64F: // emoticons
		return true
	case r >= 0x1F300 && r <= 0x1F5FF: // misc symbols & pictographs
		return true
	case r >= 0x1F680 && r <= 0x1F6FF: // transport & map
		return true
	case r >= 0x1F700 && r <= 0x1F77F: // alchemical
		return true
	case r >= 0x1F900 && r <= 0x1F9FF: // supplemental symbols & pictographs
		return true
	case r >= 0x2600 && r <= 0x26FF: // misc symbols
		return true
	case r >= 0x2700 && r <= 0x27BF: // dingbats
		return true
	}
	return false
}
