package main

import (
	"context"
	"crypto/tls"
	"embed"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/execenv"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/winstate"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/dock"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// assets embeds the Vite-built frontend. At build time, wails3 build
// compiles frontend/dist and this embed directive picks it up.
//
//go:embed all:frontend/dist
var assets embed.FS

func configPath() string {
	if p := os.Getenv("KLYX_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// No resolvable home: fail loudly instead of silently degrading to
		// "/.config/klyx/fleet.yaml" (a root-anchored path nobody intends).
		log.Fatalf("cannot resolve home directory for the fleet config: %v (set KLYX_CONFIG to override)", err)
	}
	return home + "/.config/klyx/fleet.yaml"
}

// windowStatePath lives beside the fleet config so a KLYX_CONFIG override
// relocates both together.
func windowStatePath() string {
	return filepath.Join(filepath.Dir(configPath()), "window.json")
}

func main() {
	execenv.ApplyDesktopToolPath()

	cfg, err := config.Load(configPath())
	if err != nil {
		log.Printf("warn: could not load fleet config (%v); starting with empty fleet", err)
		cfg = &config.Config{}
	}
	for _, w := range cfg.Warnings() {
		log.Printf("config warning: %s", w)
	}
	log.Printf("%s", cfg.Summary())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The emitter exists before any cluster starts: each cluster's health
	// Observer (1c) publishes through it from its first evaluation.
	em := &emitterAdapter{}
	reg := fleet.NewRegistry(cfg, fleet.DefaultConnFactory(clock.Real{}))
	reg.SetHealthSink(appbridge.NewHealthSink(em))
	reg.Start(ctx)

	// configSvc is built before the fleet service: the fleet DTOs resolve each
	// cluster's config (env, group, EFFECTIVE protection) from its live fleet,
	// which includes clusters hot-added through Settings.
	configSvc := appbridge.NewConfigService(configPath(), cfg, func(cc config.ClusterConfig) error {
		return reg.Add(ctx, cc)
	})

	svc := appbridge.NewFleetService(reg, appbridge.LiveClusterConfig(configSvc), time.Now)
	healthSvc := appbridge.NewHealthService(func() []*observe.Observer {
		var out []*observe.Observer
		for _, s := range reg.Snapshots() {
			if c, ok := reg.Conn(s.Name); ok {
				if h, ok := c.(interface{ Health() *observe.Observer }); ok && h.Health() != nil {
					out = append(out, h.Health())
				}
			}
		}
		return out
	})

	winOpener := &windowOpener{}

	gitopsSvc := appbridge.NewGitOpsService(
		func(name string) (appbridge.GitOpsConn, bool) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, false
			}
			return c, true
		},
		em, time.Now, time.Second,
	)

	certManagerSvc := appbridge.NewCertManagerService(
		func(name string) (appbridge.CertManagerConn, bool) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, false
			}
			return c, true
		},
		em, time.Now, time.Second,
	)

	externalSecretsSvc := appbridge.NewExternalSecretsService(
		func(name string) (appbridge.ExternalSecretsConn, bool) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, false
			}
			return c, true
		},
		em, time.Now, time.Second,
	)

	crdSvc := appbridge.NewCRDService(func(name string) (appbridge.CRDConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	gatewaySvc := appbridge.NewGatewayService(func(name string) (appbridge.GatewayConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})
	// globalReach is invoked once per global service and rebuilds the cilium-name -> fleet
	// map each call (a MeshMember read per connected cluster). Fine at fleet scale (a few
	// clusters, rare global services); if global services or cluster count grow, hoist the
	// per-source resolution into a per-topology factory to collapse K*N reads to N.
	gatewaySvc.SetGlobalReach(func(cluster, ns, name string) ([]string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		src, ok := reg.Conn(cluster)
		if !ok {
			return nil, false
		}
		srcMem, _ := src.MeshMember(ctx)
		if len(srcMem.Peers) == 0 {
			return nil, false
		}
		// Map every connected fleet cluster's Cilium name -> (fleet key, conn).
		type entry struct {
			fleetKey string
			conn     fleet.Conn
		}
		byCilium := map[string]entry{}
		for _, snap := range reg.Snapshots() {
			c, ok := reg.Conn(snap.Name)
			if !ok {
				continue
			}
			m, _ := c.MeshMember(ctx)
			if m.Identity.Name != "" {
				byCilium[m.Identity.Name] = entry{fleetKey: snap.Name, conn: c}
			}
		}
		var peers []string
		unconfirmed := false
		for _, peerCilium := range srcMem.Peers {
			e, present := byCilium[peerCilium]
			if !present {
				unconfirmed = true // off-fleet: can't inspect
				continue
			}
			if e.fleetKey == cluster {
				continue // never count the source cluster as its own peer
			}
			if e.conn.HasGlobalService(ctx, ns, name) {
				peers = append(peers, e.fleetKey)
			}
		}
		return peers, unconfirmed
	})

	meshSvc := appbridge.NewMeshService(func() []clustermesh.Member {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var members []clustermesh.Member
		for _, snap := range reg.Snapshots() {
			c, ok := reg.Conn(snap.Name)
			if !ok {
				continue
			}
			m, _ := c.MeshMember(ctx)
			members = append(members, m)
		}
		return members
	})

	metricsSvc := appbridge.NewMetricsService(func(name string) (appbridge.MetricsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	workloadsSvc := appbridge.NewWorkloadsService(func(name string) (appbridge.WorkloadsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	}, em)

	podsSvc := appbridge.NewPodsService(func(name string) (appbridge.PodsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	}, em)

	logsSvc := appbridge.NewLogsService(func(name string) (appbridge.LogsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	}, em)

	eventsSvc := appbridge.NewEventsService(func(name string) (appbridge.EventsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	}, em)

	nodesSvc := appbridge.NewNodesService(func(name string) (appbridge.NodesConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	nodeOpsSvc := appbridge.NewNodeOpsService(func(name string) (appbridge.NodeOpsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	}, em)

	forwardsSvc := appbridge.NewForwardsService(func(name string) (appbridge.ForwardsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	}, em)

	execSvc := appbridge.NewExecService(func(name string) (appbridge.ExecConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	helmSvc := appbridge.NewHelmService(func(name string) (appbridge.HelmConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	windowsSvc := appbridge.NewWindowsService(winOpener)

	// Backend gate for destructive verbs on protected clusters: the frontend
	// ConfirmDialog arms a one-shot grant (ConfigService.ArmProtected) and each
	// destructive service method consumes it via checkProtected. The protection
	// source is LIVE (configSvc.IsProtected reads the startup config plus this
	// session's hot-added clusters and infers prod), so a prd context imported
	// mid-session through Settings is gated immediately rather than fail-open
	// until a restart. Installed here, after configSvc exists; the guard is not
	// consulted until a destructive verb runs at request time.
	appbridge.SetProtectGuard(appbridge.NewProtectGuard(configSvc.IsProtected, time.Now))

	// AI-assist (read-only "Explain this"). Off unless fleet.yaml configures a
	// backend. The client is built once from the startup config; nil when off, so
	// AIService.Available() reports not-ready and the UI hides the affordance. The
	// egress gate reuses configSvc.IsProtected (live) exactly like the ProtectGuard.
	var aiClient appbridge.Completer
	aiBackend, aiAllowEgress := "off", false
	if cfg.AI.Enabled() {
		aiBackend, aiAllowEgress = cfg.AI.Backend, cfg.AI.AllowProtectedEgress
		tr := &http.Transport{}
		if cfg.AI.TLSSkipVerify {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user opt-in for a local self-signed endpoint
		}
		httpc := &http.Client{Transport: tr, Timeout: 90 * time.Second}
		aiClient = ai.NewClient(ai.Backend(cfg.AI.Backend), cfg.AI.BaseURL, cfg.AI.APIKey, cfg.AI.Model, httpc)
	}
	aiSvc := appbridge.NewAIService(
		func(name string) (appbridge.AIConn, bool) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, false
			}
			return c, true
		},
		// onMachine: only a loopback endpoint keeps cluster context on this
		// machine; any other endpoint (hosted, or "local" pointed at a LAN/hosted
		// OpenAI-compatible server) needs ai.allowProtectedEgress for protected
		// clusters.
		aiClient, aiBackend, aiClient != nil && ai.IsLoopbackEndpoint(cfg.AI.BaseURL), aiAllowEgress, configSvc.IsProtected,
		func(name string) capability.Set {
			for _, sn := range reg.Snapshots() {
				if sn.Name == name {
					return sn.Capabilities
				}
			}
			return capability.Set{}
		},
	)

	argoSvc := appbridge.NewArgoService(func(name string) (appbridge.ArgoConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	rbacSvc := appbridge.NewRBACService(func(name string) (appbridge.RBACConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	promotionSvc := appbridge.NewPromotionService(func(name string) (appbridge.PromotionConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	upgradeSvc := appbridge.NewUpgradeService(func(name string) (appbridge.UpgradeConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	connectivitySvc := appbridge.NewConnectivityService(func(name string) (appbridge.ConnectivityConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	expirySvc := appbridge.NewExpiryService(func(name string) (appbridge.ExpiryConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	karpenterSvc := appbridge.NewKarpenterService(func(name string) (appbridge.KarpenterConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	// Native shell: window title context, dock/taskbar badge, OS notifications.
	// mainWin is assigned after window creation below; the closures are nil-safe
	// until then.
	var mainWin application.Window
	// The bounds the window was created with (zero on first launch), for
	// winstate.Settle at shutdown.
	var requestedBounds winstate.Bounds
	dockSvc := dock.New()
	notifySvc := notifications.New()
	shellSvc := appbridge.NewShellService(
		func(title string) {
			if mainWin != nil {
				mainWin.SetTitle(title)
			}
		},
		dockSvc.SetBadge,
		dockSvc.RemoveBadge,
		func(title, body string) error {
			return notifySvc.SendNotification(notifications.NotificationOptions{
				ID: "klyx-action", Title: title, Body: body,
			})
		},
		notifySvc.RequestNotificationAuthorization,
	)

	app := application.New(application.Options{
		Name:        "Klyx",
		Description: "Platform-engineer-grade Kubernetes desktop client",
		// One Klyx per machine: a second instance would double the informer
		// load on every cluster and race the first on fleet.yaml writes. The
		// second launch exits after waking the running instance.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.moomora.klyx",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				log.Printf("second Klyx launch redirected to this instance")
			},
		},
		Services: []application.Service{
			application.NewService(svc),
			application.NewService(healthSvc),
			application.NewService(gitopsSvc),
			application.NewService(certManagerSvc),
			application.NewService(externalSecretsSvc),
			application.NewService(crdSvc),
			application.NewService(gatewaySvc),
			application.NewService(meshSvc),
			application.NewService(metricsSvc),
			application.NewService(workloadsSvc),
			application.NewService(podsSvc),
			application.NewService(logsSvc),
			application.NewService(eventsSvc),
			application.NewService(nodesSvc),
			application.NewService(nodeOpsSvc),
			application.NewService(forwardsSvc),
			application.NewService(execSvc),
			application.NewService(helmSvc),
			application.NewService(windowsSvc),
			application.NewService(configSvc),
			application.NewService(argoSvc),
			application.NewService(rbacSvc),
			application.NewService(promotionSvc),
			application.NewService(upgradeSvc),
			application.NewService(connectivitySvc),
			application.NewService(expirySvc),
			application.NewService(karpenterSvc),
			application.NewService(aiSvc),
			application.NewService(dockSvc),
			application.NewService(notifySvc),
			application.NewService(shellSvc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// Drain every long-lived resource on quit: port-forwards bind local OS
		// sockets/SPDY tunnels, log tails hold apiserver streams, and drains own
		// child kubectl processes - none may outlive the app.
		OnShutdown: func() {
			// Remember the window geometry for the next launch (best-effort).
			if mainWin != nil {
				x, y := mainWin.Position()
				w, h := mainWin.Size()
				// Only darwin's windowNew creates the window one point small.
				b := winstate.Settle(requestedBounds, winstate.Bounds{X: x, Y: y, Width: w, Height: h}, runtime.GOOS == "darwin")
				if err := winstate.Save(windowStatePath(), b); err != nil {
					log.Printf("warn: could not persist window state: %v", err)
				}
			}
			forwardsSvc.StopAll()
			logsSvc.CloseAll()
			nodeOpsSvc.CancelAll()
			// Live-list subscriptions hold per-cluster watch goroutines; drain them
			// so no watcher outlives the app.
			podsSvc.CloseAll()
			workloadsSvc.CloseAll()
			eventsSvc.CloseAll()
		},
	})

	em.app = app
	winOpener.app = app

	go appbridge.RunFleetPush(ctx, svc, em, time.Second)

	mainOpts := application.WebviewWindowOptions{
		Title: "Klyx",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 0,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(15, 20, 30),
		URL:              "/",
	}
	// Restore the last run's geometry: a daily driver that forgets its window
	// every morning reads as a prototype. Insane/missing state -> defaults.
	if b, ok := winstate.Load(windowStatePath()); ok {
		mainOpts.Width, mainOpts.Height = b.Width, b.Height
		mainOpts.X, mainOpts.Y = b.X, b.Y
		mainOpts.InitialPosition = application.WindowXY
		requestedBounds = b
	}
	mainWin = app.Window.NewWithOptions(mainOpts)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// emitterAdapter adapts the Wails app event API to appbridge.Emitter.
type emitterAdapter struct{ app *application.App }

func (e *emitterAdapter) Emit(name string, data any) {
	if e.app == nil {
		return
	}
	e.app.Event.Emit(name, data)
}

// windowOpener adapts the Wails window API to appbridge.WindowOpener. It opens
// auxiliary windows (pop-out log tails) on the same app handle as the main
// window. NewWithOptions is safe to call after Run has started: Wails queues the
// creation onto the main thread internally (runOrDeferToAppRun), so callers do
// not need to dispatch. New windows boot the same embedded SPA; the frontend
// branches on the `logswin` query flag to render only the log pane.
type windowOpener struct{ app *application.App }

func (w *windowOpener) OpenWindow(title, url string, width, height int) {
	if w.app == nil {
		return
	}
	w.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  title,
		URL:    url,
		Width:  width,
		Height: height,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 0,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(15, 20, 30),
	})
}
