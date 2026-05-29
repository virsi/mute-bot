package normalize

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClean_StripsLinksAndEmojis(t *testing.T) {
	in := "🔥 Breaking: smth happened https://example.com\n#hashtag @username"
	out := Clean(in)
	require.NotContains(t, out, "https://")
	require.NotContains(t, out, "🔥")
	require.NotContains(t, out, "@username")
	require.NotContains(t, out, "#hashtag")
	require.Contains(t, out, "Breaking")
}

func TestClean_CollapsesWhitespace(t *testing.T) {
	in := "Hello    world\n\n\nfoo"
	require.Equal(t, "Hello world foo", Clean(in))
}

func TestClean_StripsAdvertTrailer(t *testing.T) {
	in := "Some news.\n\nРЕКЛАМА\nКупите курс по гороскопам"
	out := Clean(in)
	require.Equal(t, "Some news.", out)
}

func TestClean_HandlesEmptyAndWhitespaceOnly(t *testing.T) {
	require.Equal(t, "", Clean(""))
	require.Equal(t, "", Clean("   \n\t\n"))
}

func TestDetectLang_Russian(t *testing.T) {
	require.Equal(t, "ru", DetectLang("Это русский текст про политику"))
}

func TestDetectLang_English(t *testing.T) {
	require.Equal(t, "en", DetectLang("This is plain English text about politics"))
}

func TestDetectLang_Undetermined(t *testing.T) {
	require.Equal(t, "und", DetectLang("12345 !!! 67890"))
}
