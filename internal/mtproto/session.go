// Package mtproto wraps the gotd Telegram client with the bootstrap and
// update-handling glue used by the session-reader binary.
package mtproto

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// SessionConfig is the minimal set of inputs required to bring up a gotd
// client backed by an on-disk session file. Code/Password are optional
// callbacks invoked only when a fresh login is needed.
type SessionConfig struct {
	APIID       int
	APIHash     string
	SessionPath string
	Phone       string
	Code        func(ctx context.Context, sent *tg.AuthSentCode) (string, error)
	Password    func(ctx context.Context) (string, error)
}

// NewClient constructs a gotd telegram.Client with a FileStorage session.
// The session directory is created with 0700 permissions if missing. The
// returned FileStorage is also returned so callers can rotate or inspect it.
func NewClient(cfg SessionConfig) (*telegram.Client, *session.FileStorage, error) {
	if cfg.SessionPath == "" {
		return nil, nil, fmt.Errorf("session path is required")
	}
	dir := filepath.Dir(cfg.SessionPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("mkdir session dir: %w", err)
		}
	}
	storage := &session.FileStorage{Path: cfg.SessionPath}
	client := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
		SessionStorage: storage,
	})
	return client, storage, nil
}

// Authenticate runs the login flow only if the local session is not yet
// authorized. Code/Password callbacks are invoked on demand.
func Authenticate(ctx context.Context, c *telegram.Client, cfg SessionConfig) error {
	codeFn := cfg.Code
	if codeFn == nil {
		codeFn = func(_ context.Context, _ *tg.AuthSentCode) (string, error) {
			return "", fmt.Errorf("auth code callback not configured")
		}
	}
	passFn := cfg.Password
	if passFn == nil {
		passFn = func(_ context.Context) (string, error) { return "", nil }
	}

	flow := auth.NewFlow(
		passwordAuth{
			phone: cfg.Phone,
			code:  auth.CodeAuthenticatorFunc(codeFn),
			pass:  passFn,
		},
		auth.SendCodeOptions{},
	)
	if err := c.Auth().IfNecessary(ctx, flow); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	return nil
}

// passwordAuth adapts a function-based password supplier to the
// auth.UserAuthenticator interface required by gotd's flow API.
type passwordAuth struct {
	phone string
	code  auth.CodeAuthenticator
	pass  func(ctx context.Context) (string, error)
}

func (p passwordAuth) Phone(_ context.Context) (string, error)    { return p.phone, nil }
func (p passwordAuth) Password(ctx context.Context) (string, error) { return p.pass(ctx) }

func (p passwordAuth) Code(ctx context.Context, sent *tg.AuthSentCode) (string, error) {
	return p.code.Code(ctx, sent)
}

func (p passwordAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	return &auth.SignUpRequired{TermsOfService: tos}
}

func (p passwordAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("signup not supported for session-reader")
}
