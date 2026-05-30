package mtproto

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/peers"
)

// NormalizeUsername strips common decorations from a Telegram channel
// reference and returns the bare username. Accepts @-prefix, t.me/ links,
// and surrounding whitespace. Returns "" for inputs that yield no name.
func NormalizeUsername(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "t.me/")
	s = strings.TrimPrefix(s, "@")
	s = strings.TrimSuffix(s, "/")
	return s
}

// ChannelRef is the minimal projection of a Telegram channel that the
// session-reader and catchup code needs to identify and address a channel.
type ChannelRef struct {
	Username   string
	ChannelID  int64
	AccessHash int64
	Title      string
}

// ChannelResolver wraps gotd's peers.Manager with username resolution
// returning ChannelRef. Callers must hold a fully authenticated client.
type ChannelResolver struct {
	mgr *peers.Manager
}

// NewChannelResolver constructs a ChannelResolver backed by gotd peers.
func NewChannelResolver(c *telegram.Client) *ChannelResolver {
	return &ChannelResolver{mgr: peers.Options{}.Build(c.API())}
}

// ResolveUsername normalizes the input, calls Telegram's ResolveDomain,
// and asserts the result is a channel (not a user / chat).
func (r *ChannelResolver) ResolveUsername(ctx context.Context, username string) (ChannelRef, error) {
	uname := NormalizeUsername(username)
	if uname == "" {
		return ChannelRef{}, fmt.Errorf("empty username")
	}
	peer, err := r.mgr.ResolveDomain(ctx, uname)
	if err != nil {
		return ChannelRef{}, fmt.Errorf("resolve %s: %w", uname, err)
	}
	ch, ok := peer.(peers.Channel)
	if !ok {
		return ChannelRef{}, fmt.Errorf("%s is not a channel", uname)
	}
	raw := ch.Raw()
	return ChannelRef{
		Username:   uname,
		ChannelID:  raw.ID,
		AccessHash: raw.AccessHash,
		Title:      raw.Title,
	}, nil
}
