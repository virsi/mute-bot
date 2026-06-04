package tgscraper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const sampleHTML = `
<html><body>
<div class="tgme_widget_message_wrap">
  <div class="tgme_widget_message" data-post="bbcrussian/100">
    <div class="tgme_widget_message_text">Первая новость</div>
    <a class="tgme_widget_message_date" href="https://t.me/bbcrussian/100">
      <time datetime="2026-06-04T10:00:00+00:00">10:00</time>
    </a>
  </div>
  <div class="tgme_widget_message" data-post="bbcrussian/101">
    <div class="tgme_widget_message_text">Вторая новость с <b>жирным</b> текстом</div>
    <a class="tgme_widget_message_date" href="https://t.me/bbcrussian/101">
      <time datetime="2026-06-04T11:30:45+00:00">11:30</time>
    </a>
  </div>
  <div class="tgme_widget_message" data-post="bbcrussian/102">
    <!-- media-only, no text — must be skipped -->
    <a class="tgme_widget_message_date" href="https://t.me/bbcrussian/102">
      <time datetime="2026-06-04T12:00:00+00:00">12:00</time>
    </a>
  </div>
</div>
</body></html>`

func TestParseChannelHTML_ExtractsTextPostsOnly(t *testing.T) {
	posts, err := ParseChannelHTML(sampleHTML, "bbcrussian")
	require.NoError(t, err)
	require.Len(t, posts, 2, "media-only post must be skipped")

	require.Equal(t, int64(100), posts[0].TGMsgID)
	require.Equal(t, "Первая новость", posts[0].Text)
	require.Equal(t, time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC), posts[0].PostedAt)

	require.Equal(t, int64(101), posts[1].TGMsgID)
	require.Contains(t, posts[1].Text, "жирным")

	// ChannelID is the FNV hash of the username — stable across calls.
	require.Equal(t, posts[0].ChannelID, posts[1].ChannelID)
	require.Equal(t, PseudoChannelID("BBCRussian"), posts[0].ChannelID,
		"PseudoChannelID must be case-insensitive")
}

func TestParseChannelHTML_EmptyDocument(t *testing.T) {
	posts, err := ParseChannelHTML("<html><body></body></html>", "any")
	require.NoError(t, err)
	require.Empty(t, posts)
}

func TestPseudoChannelID_Stable(t *testing.T) {
	a := PseudoChannelID("meduzalive")
	b := PseudoChannelID("meduzalive")
	require.Equal(t, a, b)
	require.Greater(t, a, int64(0), "must be positive (signed int64)")
}
