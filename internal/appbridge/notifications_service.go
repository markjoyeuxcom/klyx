package appbridge

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/healthnotify"
	"github.com/moomora/klyx/internal/observe"
)

// Events the notifications service emits to the webview.
const (
	// HealthBaselineEvent lists faults present at connect or after a gap,
	// once, in the app - never an OS notification.
	HealthBaselineEvent = "health:baseline"
	// HealthNotificationOpenEvent asks the app to open a notified object,
	// pinned to its cluster and UID (or a burst's cluster).
	HealthNotificationOpenEvent = "health:notification-open"
)

// notifyClock is the engine's clock, and notifyRoleCap the Observer's
// RoleCap the engine's evidence is bounded by (0: the Observer's default):
// test seams.
var (
	notifyClock   = time.Now
	notifyRoleCap = 0
)

// NotificationsService runs health notifications (The Line slice 10, parent
// §8.6): it listens to the transition stream, ticks the episode engine,
// sends through the OS notification service and tells the webview of
// baselines and clicks. Its settings are fleet.yaml's notifications block,
// written only by Settings > Notifications.
type NotificationsService struct {
	engine    *healthnotify.Engine
	path      string
	send      func(id, title, body string, data map[string]any) error
	authorize func() (bool, error)
	emit      func(name string, data any)

	mu       sync.Mutex
	settings config.NotificationsConfig
	stop     chan struct{}
	// recent keeps the last listings, so a webview that subscribes after a
	// cluster connected still lists its faults once (subscribe, then read).
	recent []HealthBaselineDTO
	seq    uint64
}

// recentBaselines bounds the listings kept for a late subscriber.
const recentBaselines = 20

// NotificationSettingsDTO is the Settings > Notifications form.
type NotificationSettingsDTO struct {
	Enabled    bool   `json:"enabled"`
	Scope      string `json:"scope"` // "protected" | "all"
	Recoveries bool   `json:"recoveries"`
}

// NotificationObjectDTO names a notified object, to open it pinned.
type NotificationObjectDTO struct {
	Cluster   string `json:"cluster"`
	Task      string `json:"task,omitempty"`
	Part      string `json:"part,omitempty"`
	GVR       string `json:"gvr,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Label     string `json:"label,omitempty"`
}

// HealthBaselineDTO is one in-app listing.
type HealthBaselineDTO struct {
	Seq     uint64                  `json:"seq"` // the frontend lists each once
	Cluster string                  `json:"cluster"`
	Reason  string                  `json:"reason"` // connect | after-cap | after-gap
	Count   int                     `json:"count"`
	Items   []NotificationObjectDTO `json:"items"`
}

func NewNotificationsService(path string, cfg *config.Config, cluster func(string) (config.ClusterConfig, bool),
	send func(id, title, body string, data map[string]any) error, authorize func() (bool, error), emit func(string, any)) *NotificationsService {
	s := &NotificationsService{path: path, send: send, authorize: authorize, emit: emit, stop: make(chan struct{})}
	if cfg != nil && cfg.Notifications != nil {
		s.settings = *cfg.Notifications
	}
	s.engine = healthnotify.New(func() time.Time { return notifyClock() }, healthnotify.Hooks{
		Send: s.deliver,
		List: s.list,
		Protected: func(name string) bool {
			cc, ok := cluster(name)
			return ok && cc.IsProtected()
		},
		Place: func(name string) string {
			cc, ok := cluster(name)
			if !ok {
				return name
			}
			var parts []string
			for _, p := range []string{cc.Environment, cc.Tags["region"]} {
				if p != "" {
					parts = append(parts, p)
				}
			}
			if len(parts) == 0 {
				return name
			}
			return name + " · " + strings.Join(parts, " · ")
		},
	}, notifyRoleCap)
	s.engine.SetSettings(engineSettings(s.settings))
	return s
}

func engineSettings(c config.NotificationsConfig) healthnotify.Settings {
	return healthnotify.Settings{Enabled: c.Enabled, Scope: c.Scope, Recoveries: c.Recoveries}
}

// The service's own lifecycle and inputs are package functions, not
// methods: every exported method of a bound service reaches the webview.

// RunNotifications ticks the engine once a second until StopNotifications.
func RunNotifications(s *NotificationsService) {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.engine.Flush()
			}
		}
	}()
}

// StopNotifications ends the ticker.
func StopNotifications(s *NotificationsService) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

// NotificationsListener is the HealthListener feeding the engine.
func NotificationsListener(s *NotificationsService) HealthListener { return notifyListener{s} }

type notifyListener struct{ s *NotificationsService }

func (l notifyListener) OnTransitions(b observe.TransitionBatch) { l.s.engine.Apply(b) }
func (l notifyListener) OnSummary(observe.Summary)               {}
func (l notifyListener) OnRows(observe.Rows)                     {}

// Settings returns the current settings.
func (s *NotificationsService) Settings() NotificationSettingsDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := s.settings.Scope
	if scope != "all" {
		scope = "protected"
	}
	return NotificationSettingsDTO{Enabled: s.settings.Enabled, Scope: scope, Recoveries: s.settings.Recoveries}
}

// SetSettings writes the notifications block and applies it. Enabling
// requests the OS permission at that moment; a refusal keeps the setting
// and says so.
func (s *NotificationsService) SetSettings(d NotificationSettingsDTO) ActionResultDTO {
	c := config.NotificationsConfig{Enabled: d.Enabled, Scope: d.Scope, Recoveries: d.Recoveries}
	if c.Scope != "all" {
		c.Scope = "protected"
	}
	if err := config.SaveNotifications(s.path, c); err != nil {
		return ActionResultDTO{OK: false, Error: err.Error()}
	}
	s.mu.Lock()
	s.settings = c
	s.mu.Unlock()
	s.engine.SetSettings(engineSettings(c))
	if c.Enabled && s.authorize != nil {
		if ok, err := s.authorize(); err != nil || !ok {
			return ActionResultDTO{OK: true, Error: "the OS has notifications off for Klyx: nothing will be shown until they are allowed"}
		}
	}
	return ActionResultDTO{OK: true}
}

// SetFocus records whether the app is focused, and on which cluster: nothing
// is sent while the app shows that cluster.
func (s *NotificationsService) SetFocus(cluster string, focused bool) {
	s.engine.SetFocus(cluster, focused)
}

func objectDTO(o healthnotify.Object) NotificationObjectDTO {
	return NotificationObjectDTO{Cluster: o.Cluster, Task: o.Task, Part: o.Part, GVR: o.GVR, Namespace: o.Namespace, Name: o.Name, UID: o.UID, Kind: o.Kind, Label: o.Label}
}

// deliver sends through the OS; its error tells the engine the onset was
// not sent, so its recovery will not notify.
func (s *NotificationsService) deliver(n healthnotify.Notification) error {
	if s.send == nil {
		return fmt.Errorf("no notification service")
	}
	data := map[string]any{"cluster": n.Cluster}
	id := fmt.Sprintf("klyx-health-%s-%d", n.Cluster, time.Now().UnixNano())
	if o := n.Single; o != nil {
		data = map[string]any{"cluster": o.Cluster, "task": o.Task, "part": o.Part, "gvr": o.GVR, "namespace": o.Namespace, "name": o.Name, "uid": o.UID}
	}
	return s.send(id, n.Title, n.Body, data)
}

func (s *NotificationsService) list(b healthnotify.Baseline) {
	d := HealthBaselineDTO{Cluster: b.Cluster, Reason: b.Reason, Count: b.Count, Items: make([]NotificationObjectDTO, 0, len(b.Items))}
	for _, o := range b.Items {
		d.Items = append(d.Items, objectDTO(o))
	}
	s.mu.Lock()
	s.seq++
	d.Seq = s.seq
	s.recent = append(s.recent, d)
	if len(s.recent) > recentBaselines {
		s.recent = s.recent[len(s.recent)-recentBaselines:]
	}
	s.mu.Unlock()
	if s.emit != nil {
		s.emit(HealthBaselineEvent, d)
	}
}

// RecentBaselines returns the last listings: a webview subscribes to the
// event, then reads these, and lists each seq once.
func (s *NotificationsService) RecentBaselines() []HealthBaselineDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]HealthBaselineDTO{}, s.recent...)
}

// NotificationClicked handles a notification's click: its Data names the
// object (or a burst's cluster), which the webview opens pinned.
func NotificationClicked(s *NotificationsService, data map[string]any) {
	if s.emit == nil || data == nil {
		return
	}
	str := func(k string) string {
		v, _ := data[k].(string)
		return v
	}
	if str("cluster") == "" {
		return
	}
	s.emit(HealthNotificationOpenEvent, NotificationObjectDTO{Cluster: str("cluster"), Task: str("task"), Part: str("part"), GVR: str("gvr"),
		Namespace: str("namespace"), Name: str("name"), UID: str("uid")})
}
