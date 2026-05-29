package normalize

import "unicode"

// DetectLang returns "ru" if Cyrillic runes dominate s, "en" if Latin runes
// dominate, and "und" otherwise (mixed equally or no letters).
//
// This is a cheap heuristic — good enough for routing posts to RU/EN paths.
// For finer-grained language detection, swap in a real classifier later.
func DetectLang(s string) string {
	var cyr, lat int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			cyr++
		case unicode.Is(unicode.Latin, r):
			lat++
		}
	}
	switch {
	case cyr > lat:
		return "ru"
	case lat > cyr:
		return "en"
	default:
		return "und"
	}
}
