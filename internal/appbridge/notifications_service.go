package appbridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
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

// notifyClock is the engine's clock and notifyRoleCap the Observer's
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
// written only by Settings > Notifications, and the running copy the engine
// uses, replaced only by an Apply (Settings slice F §5): a hand edit of the
// block is drift until "Use the file's values" or a restart.
type NotificationsService struct {
	engine    *healthnotify.Engine
	path      string
	send      func(id, title, body string, data map[string]any) error
	authorize func() (bool, error)
	emit      func(name string, data any)

	// setMu makes a settings change one operation: the plan, the write, the
	// running copy and the engine, so a later change never lands under an
	// earlier one.
	setMu sync.Mutex
	load  func(string) (*config.NotificationsConfig, string, error)
	save  func(path, version string, n config.NotificationsConfig) (string, error)
	cache *planCache
	mu    sync.Mutex
	// settings is the running copy, normalized; rev moves with each
	// activation.
	settings config.NotificationsConfig
	rev      uint64
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
	s := &NotificationsService{path: path, send: send, authorize: authorize, emit: emit, stop: make(chan struct{}),
		load: loadNotifyBlock, save: config.SaveNotificationsAt, cache: newPlanCache(time.Now)}
	s.settings = config.NormalNotifications(nil)
	if cfg != nil {
		s.settings = config.NormalNotifications(cfg.Notifications)
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

// ForgetCluster drops a removed cluster's episodes silently (S2 §2.5).
func (l notifyListener) ForgetCluster(cluster string) { l.s.engine.Forget(cluster) }
func (l notifyListener) OnSummary(observe.Summary)    {}
func (l notifyListener) OnRows(observe.Rows)          {}

// NotificationsStateDTO is Settings > Notifications: the settings in use,
// and whether fleet.yaml's block differs from them (Settings slice F §5).
type NotificationsStateDTO struct {
	InUse NotificationSettingsDTO `json:"inUse"`
	// Diverged: fleet.yaml's block is not the one in use (a hand edit);
	// "Use the file's values" plans it
	Diverged  bool   `json:"diverged"`
	FileError string `json:"fileError,omitempty"`
}

func loadNotifyBlock(path string) (*config.NotificationsConfig, string, error) {
	cfg, version, err := config.LoadVersioned(path)
	if err != nil {
		return nil, version, err
	}
	return cfg.Notifications, version, nil
}

func notifyDTO(c config.NotificationsConfig) NotificationSettingsDTO {
	return NotificationSettingsDTO{Enabled: c.Enabled, Scope: c.Scope, Recoveries: c.Recoveries}
}

// running is the running copy and its revision.
func (s *NotificationsService) running() (config.NotificationsConfig, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, s.rev
}

// Settings returns the settings in use, and whether the file's differ.
func (s *NotificationsService) Settings() NotificationsStateDTO {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	run, _ := s.running()
	st := NotificationsStateDTO{InUse: notifyDTO(run)}
	cur, _, err := s.load(s.path)
	if err != nil {
		st.FileError = config.DisplayError(err)
		return st
	}
	st.Diverged = config.NormalNotifications(cur) != run
	return st
}

// notifyDiverged: a change starts from what runs, so writing it would
// replace a hand edit it never showed.
const notifyDiverged = "fleet.yaml's notification settings differ from the ones in use, so nothing was written: use the file's values first, or undo that edit"

// notifyCandidate is what a plan's digest holds in the cache.
type notifyCandidate struct {
	Kind string                     `json:"kind"`
	Cand config.NotificationsConfig `json:"cand"`
}

func notifyRefused(kind, why string) PlanDTO {
	return PlanDTO{Section: "notifications", Kind: kind, Refused: why, Runtime: map[string]string{}, Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
}

// planLocked plans cand from what runs. It ends nothing and needs no
// interlock (F2). A change is refused while the file's block differs from
// the one in use: the block condition, which the write's file version then
// holds to.
func (s *NotificationsService) planLocked(kind string, cand config.NotificationsConfig, version string, file config.NotificationsConfig) PlanDTO {
	run, rev := s.running()
	p := PlanDTO{Section: "notifications", Kind: kind, FileVersion: version, Changes: notifyChanges(run, cand), Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{},
		Activates: cand != run, Writes: kind == planSave && cand != file}
	if kind == planSave && file != run {
		p.Refused = notifyDiverged
	}
	p.Runtime = map[string]string{"notifications": strconv.FormatUint(rev, 10)}
	c := notifyCandidate{Kind: kind, Cand: cand}
	bind, _ := json.Marshal(c)
	seal(&p, string(bind))
	if p.Refused == "" {
		s.cache.put(p.Digest, c)
	}
	return p
}

// candidate is c's candidate as it stands now: "Use the file's values"
// adopts the file as it is.
func (c notifyCandidate) candidate(file config.NotificationsConfig) config.NotificationsConfig {
	if c.Kind == planFile {
		return file
	}
	return c.Cand
}

// ApplyNotificationsQuiet is a toggle (Settings slice F §6): planned from
// what runs and applied at once when, at Apply, the plan ends nothing and
// needs no interlock - which in F2 it never does; otherwise the plan comes
// back for review. Enabling asks the OS for permission at that moment; a
// refusal keeps the setting and says so.
func (s *NotificationsService) ApplyNotificationsQuiet(d NotificationSettingsDTO) ApplyResultDTO {
	cand := config.NormalNotifications(&config.NotificationsConfig{Enabled: d.Enabled, Scope: d.Scope, Recoveries: d.Recoveries})
	s.setMu.Lock()
	r := func() ApplyResultDTO {
		cur, version, err := s.load(s.path)
		if err != nil {
			return ApplyResultDTO{Error: "fleet.yaml cannot be read, so nothing was saved: " + config.DisplayError(err)}
		}
		p := s.planLocked(planSave, cand, version, config.NormalNotifications(cur))
		if p.Refused != "" {
			return ApplyResultDTO{Error: p.Refused}
		}
		return s.applyLocked(p.Digest, true)
	}()
	s.setMu.Unlock()
	return s.permission(r)
}

// PlanNotificationsFromFile plans "Use the file's values": the file's
// block, from what runs, writing nothing.
func (s *NotificationsService) PlanNotificationsFromFile() PlanDTO {
	s.setMu.Lock()
	defer s.setMu.Unlock()
	cur, version, err := s.load(s.path)
	if err != nil {
		return notifyRefused(planFile, "fleet.yaml cannot be read: "+config.DisplayError(err))
	}
	file := config.NormalNotifications(cur)
	return s.planLocked(planFile, file, version, file)
}

// ApplyNotifications applies the plan with digest: re-planned from what
// runs, refused with the new plan when anything moved, written against the
// plan's file version, then activated.
func (s *NotificationsService) ApplyNotifications(digest string) ApplyResultDTO {
	s.setMu.Lock()
	r := s.applyLocked(digest, false)
	s.setMu.Unlock()
	return s.permission(r)
}

func (s *NotificationsService) applyLocked(digest string, quiet bool) ApplyResultDTO {
	v, ok := s.cache.get(digest)
	if !ok {
		return ApplyResultDTO{Error: "This plan expired or was already applied: review the changes again."}
	}
	c := v.(notifyCandidate)
	cur, version, err := s.load(s.path)
	if err != nil {
		return ApplyResultDTO{Error: "fleet.yaml cannot be read, so nothing was applied: " + config.DisplayError(err)}
	}
	file := config.NormalNotifications(cur)
	cand := c.candidate(file)
	re := s.planLocked(c.Kind, cand, version, file)
	switch {
	case re.Digest != digest:
		return ApplyResultDTO{Error: "Something changed since this was planned: review the new plan.", Plan: &re}
	case re.Refused != "":
		// unreachable while a refused plan is never cached and its refusal
		// is in its digest; kept so no refused plan can ever apply
		return ApplyResultDTO{Error: re.Refused}
	case quiet && (len(re.Ends) > 0 || re.Interlock != nil):
		return ApplyResultDTO{Plan: &re}
	}
	if re.Writes {
		_, err := s.save(s.path, version, cand)
		if errors.Is(err, config.ErrFileChanged) {
			return ApplyResultDTO{Error: "fleet.yaml changed while this was applied, so nothing was written: review the new plan.", Plan: s.replanLocked(c)}
		}
		if err != nil {
			return ApplyResultDTO{Error: config.DisplayError(err)}
		}
	}
	s.cache.consume(digest)
	if re.Activates {
		s.mu.Lock()
		s.settings = cand
		s.rev++
		s.mu.Unlock()
		s.engine.SetSettings(engineSettings(cand))
	}
	return ApplyResultDTO{OK: true}
}

// replanLocked is c's plan from the file as it is now.
func (s *NotificationsService) replanLocked(c notifyCandidate) *PlanDTO {
	cur, version, err := s.load(s.path)
	if err != nil {
		p := notifyRefused(c.Kind, "fleet.yaml cannot be read: "+config.DisplayError(err))
		return &p
	}
	file := config.NormalNotifications(cur)
	p := s.planLocked(c.Kind, c.candidate(file), version, file)
	return &p
}

// permission asks the OS once an applied state is enabled, outside the
// settings lock; a refusal keeps the setting and says so.
func (s *NotificationsService) permission(r ApplyResultDTO) ApplyResultDTO {
	if run, _ := s.running(); !r.OK || !run.Enabled || s.authorize == nil {
		return r
	}
	if ok, err := s.authorize(); err != nil || !ok {
		r.Note = "the OS has notifications off for Klyx: nothing will be shown until they are allowed"
	}
	return r
}

// notifyChanges are the effective changes from what runs to cand, by
// registry id.
func notifyChanges(from, to config.NotificationsConfig) []PlanChangeDTO {
	out := []PlanChangeDTO{}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	scope := func(v string) string {
		if v == "all" {
			return "every cluster"
		}
		return "protected clusters only"
	}
	if from.Enabled != to.Enabled {
		out = append(out, PlanChangeDTO{ID: "notifications.enabled", Label: "Notify me about new faults", From: onOff(from.Enabled), To: onOff(to.Enabled)})
	}
	if from.Scope != to.Scope {
		out = append(out, PlanChangeDTO{ID: "notifications.scope", Label: "Clusters", From: scope(from.Scope), To: scope(to.Scope)})
	}
	if from.Recoveries != to.Recoveries {
		out = append(out, PlanChangeDTO{ID: "notifications.recoveries", Label: "Also notify recoveries", From: onOff(from.Recoveries), To: onOff(to.Recoveries)})
	}
	return out
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
