// Package tgscraper provides a lightweight HTML-based ingest path for public
// Telegram channels. It parses the public preview page (https://t.me/s/<username>)
// and emits domain.RawPost values onto the same NATS subject as the MTProto
// session reader, so the rest of the pipeline does not change.
//
// Limitations: public channels only, polling cadence, ~20 most recent posts
// per page. Intended as a stop-gap until a real MTProto session is available.
package tgscraper

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/virsi/mute-bot/internal/domain"
)

// PseudoChannelID derives a stable int64 from a channel username. We use it as
// the RawPost.ChannelID surrogate when we have no real MTProto channel id;
// downstream code only requires stability + uniqueness.
func PseudoChannelID(username string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(username)))
	// Mask to 63 bits so the value fits a signed int64 without going negative.
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

// ParseChannelHTML extracts all posts visible on the public t.me/s/<username>
// preview page. Posts are returned in document order (oldest at the top of
// the visible window, newest at the bottom).
func ParseChannelHTML(htmlBody, username string) ([]domain.RawPost, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlBody))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	chID := PseudoChannelID(username)
	posts := make([]domain.RawPost, 0, 32)

	doc.Find(".tgme_widget_message[data-post]").Each(func(_ int, sel *goquery.Selection) {
		dataPost, _ := sel.Attr("data-post")
		// data-post is "username/msg_id"
		_, msgIDStr, ok := strings.Cut(dataPost, "/")
		if !ok {
			return
		}
		msgID, err := strconv.ParseInt(msgIDStr, 10, 64)
		if err != nil || msgID <= 0 {
			return
		}

		text := strings.TrimSpace(sel.Find(".tgme_widget_message_text").First().Text())
		if text == "" {
			// Media-only posts (photo/video without caption) — skip.
			return
		}

		posted := time.Now().UTC()
		if iso, ok := sel.Find(".tgme_widget_message_date time").First().Attr("datetime"); ok {
			if t, err := time.Parse(time.RFC3339, iso); err == nil {
				posted = t.UTC()
			}
		}

		posts = append(posts, domain.RawPost{
			ChannelID: chID,
			TGMsgID:   msgID,
			Text:      text,
			PostedAt:  posted,
		})
	})
	return posts, nil
}
