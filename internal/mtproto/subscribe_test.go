package mtproto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeUsername(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"@ria_novosti", "ria_novosti"},
		{"ria_novosti", "ria_novosti"},
		{"https://t.me/ria_novosti", "ria_novosti"},
		{"http://t.me/ria_novosti", "ria_novosti"},
		{"t.me/ria_novosti", "ria_novosti"},
		{"  @ria_novosti  ", "ria_novosti"},
		{"https://t.me/ria_novosti/", "ria_novosti"},
		{"", ""},
		{"@", ""},
	}
	for _, c := range cases {
		require.Equal(t, c.want, NormalizeUsername(c.in), "in=%q", c.in)
	}
}
