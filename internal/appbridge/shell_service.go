package appbridge

import (
	"fmt"
	"strings"
	"sync"
)

// ShellService bridges the frontend to native-shell concerns: the window
// title (so ⌘-Tab and the taskbar say WHERE you are, not just "Klyx"), the
// dock/taskbar badge (fleet trouble stays visible while Klyx is backgrounded),
// and OS notifications (action outcomes that land while the window is not
// focused). Dependencies are injected as narrow funcs so the service tests
// without a Wails runtime. Every method is nil-safe and best-effort: shell
// chrome must never break an operation.
type ShellService struct {
	setTitle    func(title string)
	setBadge    func(label string) error
	removeBadge func() error
	notify      func(title, body string) error
	authorize   func() (bool, error)

	authOnce   sync.Once
	authorized bool
}

func NewShellService(
	setTitle func(string),
	setBadge func(string) error,
	removeBadge func() error,
	notify func(title, body string) error,
	authorize func() (bool, error),
) *ShellService {
	return &ShellService{setTitle: setTitle, setBadge: setBadge, removeBadge: removeBadge, notify: notify, authorize: authorize}
}

// SetMainTitle renames the main window. Empty titles are ignored; long ones
// are truncated - a title bar is a label, not a log line.
func (s *ShellService) SetMainTitle(title string) {
	if s == nil || s.setTitle == nil {
		return
	}
	t := strings.TrimSpace(title)
	if t == "" {
		return
	}
	if len(t) > 200 {
		t = t[:200]
	}
	s.setTitle(t)
}

// SetBadge shows count on the dock/taskbar icon; zero or negative clears it.
func (s *ShellService) SetBadge(count int) {
	if s == nil {
		return
	}
	if count <= 0 {
		if s.removeBadge != nil {
			_ = s.removeBadge()
		}
		return
	}
	if s.setBadge != nil {
		_ = s.setBadge(fmt.Sprintf("%d", count))
	}
}

// Notify sends one OS notification. Authorization (a macOS prompt) is
// requested lazily on the first use - never at boot for a feature the user
// may not want. Failures return honestly; the frontend treats them as
// "in-app toast only".
func (s *ShellService) Notify(title, body string) ActionResultDTO {
	if s == nil || s.notify == nil {
		return ActionResultDTO{OK: false, Error: "notifications unavailable"}
	}
	if title == "" {
		return ActionResultDTO{OK: false, Error: "empty notification title"}
	}
	if s.authorize != nil {
		s.authOnce.Do(func() {
			ok, err := s.authorize()
			s.authorized = ok && err == nil
		})
		if !s.authorized {
			return ActionResultDTO{OK: false, Error: "notifications not authorized"}
		}
	}
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	if err := s.notify(title, body); err != nil {
		return ActionResultDTO{OK: false, Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}
