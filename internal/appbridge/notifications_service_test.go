package appbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/observe"
)

// The Line slice 10a: the service between the transition stream, the
// engine, fleet.yaml, the OS and the webview.
func TestNotificationsService(t *testing.T) {
	start := time.Now()
	var offset time.Duration
	defer func(c func() time.Time) { notifyClock = c }(notifyClock)
	notifyClock = func() time.Time { return start.Add(offset) }
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: prd\n    context: prd\n    environment: prd\n    protected: true\n    tags:\n      region: northeurope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	type sent struct {
		id, title, body string
		data            map[string]any
	}
	var out []sent
	var events []string
	var lastEvent any
	authorized := false
	s := NewNotificationsService(path, cfg, func(n string) (config.ClusterConfig, bool) {
		for _, c := range cfg.Clusters {
			if c.Name == n {
				return c, true
			}
		}
		return config.ClusterConfig{}, false
	}, func(id, title, body string, data map[string]any) error {
		out = append(out, sent{id, title, body, data})
		return nil
	}, func() (bool, error) { return authorized, nil }, func(name string, d any) { events = append(events, name); lastEvent = d })

	if st := s.Settings(); st.Enabled || st.Scope != "protected" {
		t.Fatalf("absent block: off, protected: %+v", st)
	}
	// enabling writes the block and asks the OS; a refusal keeps the setting and says so
	if r := s.SetSettings(NotificationSettingsDTO{Enabled: true, Scope: "protected"}); !r.OK || !strings.Contains(r.Error, "notifications off") {
		t.Fatalf("refused: %+v", r)
	}
	if c, _ := config.Load(path); c.Notifications == nil || !c.Notifications.Enabled {
		t.Fatal("not written")
	}
	authorized = true
	if r := s.SetSettings(NotificationSettingsDTO{Enabled: true, Scope: "protected"}); !r.OK || r.Error != "" {
		t.Fatalf("allowed: %+v", r)
	}
	l := NotificationsListener(s)
	base := observe.TransitionBatch{Cluster: "prd", Incarnation: 1, Task: "pods", Part: "pods", Source: "pods", GVR: "v1/pods"}
	b := base
	b.Kind, b.Items = observe.BatchBaseline, []observe.Transition{{Key: "u0", NS: "shop", Name: "old", To: "fault", From: observe.StateAbsent}}
	l.OnTransitions(b)
	b = base
	b.Kind, b.Items = observe.BatchChanges, []observe.Transition{{Key: "u1", NS: "shop", Name: "web", From: "ok", To: "fault", Label: "CrashLoopBackOff"}}
	l.OnTransitions(b)
	// past the baseline and burst windows
	offset = 2 * time.Minute
	s.engine.Flush()
	if len(events) != 1 || events[0] != HealthBaselineEvent || lastEvent.(HealthBaselineDTO).Count != 1 {
		t.Fatalf("baseline listing: %v %+v", events, lastEvent)
	}
	// a late webview reads what it missed
	if r := s.RecentBaselines(); len(r) != 1 || r[0].Seq != 1 || r[0].Items[0].UID != "u0" {
		t.Fatalf("recent %+v", r)
	}
	if len(out) != 1 || out[0].title != "prd · prd · northeurope" || out[0].body != "Pod shop/web: CrashLoopBackOff" || out[0].data["uid"] != "u1" || out[0].data["task"] != "pods" || !strings.HasPrefix(out[0].id, "klyx-health-prd-") {
		t.Fatalf("sent %+v", out)
	}
	// a click opens the object pinned
	NotificationClicked(s, out[0].data)
	if events[len(events)-1] != HealthNotificationOpenEvent || lastEvent.(NotificationObjectDTO).UID != "u1" {
		t.Fatalf("click: %v %+v", events, lastEvent)
	}
}

// The Line slice 10a end to end: a real Observer over certificates feeds
// the service through the HealthSink. The fault present at connect is
// listed, never notified; a later onset is; a recovery with recoveries off
// is not.
func TestNotificationsEndToEnd(t *testing.T) {
	start := time.Now()
	var offset atomic.Int64
	defer func(c func() time.Time) { notifyClock = c }(notifyClock)
	notifyClock = func() time.Time { return start.Add(time.Duration(offset.Load())) }
	var mu sync.Mutex
	var sent []string
	var lists []HealthBaselineDTO
	s := NewNotificationsService(filepath.Join(t.TempDir(), "fleet.yaml"), &config.Config{Notifications: &config.NotificationsConfig{Enabled: true, Scope: "all"}},
		func(string) (config.ClusterConfig, bool) { return config.ClusterConfig{}, false },
		func(_, _, body string, _ map[string]any) error {
			mu.Lock()
			sent = append(sent, body)
			mu.Unlock()
			return nil
		},
		nil,
		func(name string, d any) {
			if b, ok := d.(HealthBaselineDTO); ok {
				mu.Lock()
				lists = append(lists, b)
				mu.Unlock()
			}
		})
	served := &atomic.Bool{}
	served.Store(true)
	h := startCertObserverServed(t, "e2e", nil, served, NotificationsListener(s))
	flush := func(d time.Duration) { offset.Add(int64(d)); s.engine.Flush() }
	waitRows(t, "the connect's listing", func() bool {
		flush(time.Second)
		mu.Lock()
		defer mu.Unlock()
		return len(lists) == 1
	})
	mu.Lock()
	if lists[0].Reason != "connect" || lists[0].Count != 1 || lists[0].Items[0].UID != "uid-bad" {
		t.Fatalf("listing %+v", lists)
	}
	mu.Unlock()
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	if _, err := h.dyn.Resource(gvr).Namespace("shop").Update(context.Background(), certObj("ok", "False"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dyn.Resource(gvr).Namespace("shop").Update(context.Background(), certObj("bad", "True"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitRows(t, "the onset's notification", func() bool {
		flush(10 * time.Second)
		mu.Lock()
		defer mu.Unlock()
		return len(sent) > 0
	})
	flush(time.Hour)
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "Certificate shop/ok") {
		t.Fatalf("sent %v", sent)
	}
}

// The Observer's own cap is the engine's replacement checkpoint (slice 10
// note §3.4): a real Observer with RoleCap 3 goes over it - a capped loss -
// and back under it - an after-cap baseline - and a later onset notifies,
// which it could not if the partition were still lost.
func TestNotificationsAfterTheObserversCap(t *testing.T) {
	start := time.Now()
	var offset atomic.Int64
	defer func(c func() time.Time, rc, hc int) { notifyClock, notifyRoleCap, harnessRoleCap = c, rc, hc }(notifyClock, notifyRoleCap, harnessRoleCap)
	notifyClock = func() time.Time { return start.Add(time.Duration(offset.Load())) }
	notifyRoleCap, harnessRoleCap = 3, 3
	var mu sync.Mutex
	var sent []string
	var lists []HealthBaselineDTO
	s := NewNotificationsService(filepath.Join(t.TempDir(), "fleet.yaml"), &config.Config{Notifications: &config.NotificationsConfig{Enabled: true, Scope: "all"}},
		func(string) (config.ClusterConfig, bool) { return config.ClusterConfig{}, false },
		func(_, _, body string, _ map[string]any) error {
			mu.Lock()
			sent = append(sent, body)
			mu.Unlock()
			return nil
		},
		nil,
		func(_ string, d any) {
			if b, ok := d.(HealthBaselineDTO); ok {
				mu.Lock()
				lists = append(lists, b)
				mu.Unlock()
			}
		})
	served := &atomic.Bool{}
	served.Store(true)
	seen := &batchKinds{}
	h := startCertObserverServed(t, "cap", nil, served, NotificationsListener(s), seen)
	flush := func(d time.Duration) { offset.Add(int64(d)); s.engine.Flush() }
	waitRows(t, "the connect's listing", func() bool {
		flush(time.Second)
		mu.Lock()
		defer mu.Unlock()
		return len(lists) == 1
	})
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	certs := h.dyn.Resource(gvr).Namespace("shop")
	// over the cap: a capped loss, never an onset for the fourth
	if _, err := certs.Create(context.Background(), certObj("extra", "False"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	// back under it: the after-cap baseline settles silently
	if err := certs.Delete(context.Background(), "extra", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := certs.Update(context.Background(), certObj("ok", "False"), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitRows(t, "the onset after the cap", func() bool {
		flush(10 * time.Second)
		mu.Lock()
		defer mu.Unlock()
		return len(sent) > 0
	})
	flush(time.Hour)
	mu.Lock()
	defer mu.Unlock()
	// the after-cap baseline lists the fault it still holds, once; it never notifies
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "Certificate shop/ok") || len(lists) != 2 || lists[1].Reason != "after-cap" || lists[1].Items[0].UID != "uid-bad" {
		t.Fatalf("sent %v lists %+v", sent, lists)
	}
	if k := seen.list(); !strings.Contains(k, "loss:capped") || !strings.Contains(k, "baseline:after-cap") {
		t.Fatalf("the Observer's cap was not crossed: %s", k)
	}
}

// batchKinds records the certificates partition's batch kinds.
type batchKinds struct {
	mu    sync.Mutex
	kinds []string
}

func (b *batchKinds) OnTransitions(t observe.TransitionBatch) {
	if t.Part != "certificates" {
		return
	}
	k := t.Kind
	if t.Loss != nil {
		k += ":" + t.Loss.Reason
	}
	if t.AfterCap {
		k += ":after-cap"
	}
	b.mu.Lock()
	b.kinds = append(b.kinds, k)
	b.mu.Unlock()
}
func (b *batchKinds) OnSummary(observe.Summary) {}
func (b *batchKinds) OnRows(observe.Rows)       {}
func (b *batchKinds) list() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.kinds, ",")
}

// A settings change is one operation (slice 10a review): an earlier enable
// that pauses after its write can never land its live state under a later
// disable.
func TestNotificationSettingsChangesAreSerialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: prd\n    context: prd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paused, release := make(chan struct{}), make(chan struct{})
	defer func(f func(string, config.NotificationsConfig, config.NotificationsConfig) error) {
		saveNotifications = f
	}(saveNotifications)
	saveNotifications = func(p string, c, prev config.NotificationsConfig) error {
		err := config.SaveNotifications(p, c, prev)
		if c.Enabled {
			close(paused)
			<-release
		}
		return err
	}
	s := NewNotificationsService(path, &config.Config{}, func(string) (config.ClusterConfig, bool) { return config.ClusterConfig{}, false }, nil, nil, nil)
	done := make(chan struct{}, 2)
	go func() { s.SetSettings(NotificationSettingsDTO{Enabled: true}); done <- struct{}{} }()
	<-paused
	go func() { s.SetSettings(NotificationSettingsDTO{Enabled: false}); done <- struct{}{} }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-done
	<-done
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if live := s.Settings().Enabled; c.Notifications == nil || live != c.Notifications.Enabled || live {
		t.Fatalf("live %v, written %+v: the later disable must win both", live, c.Notifications)
	}
}
