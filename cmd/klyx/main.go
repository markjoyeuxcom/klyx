package main

import (
	"context"
	"embed"
	"log"
	"os"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/execenv"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/wailsapp/wails/v3/pkg/application"
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
	home, _ := os.UserHomeDir()
	return home + "/.config/klyx/fleet.yaml"
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

	reg := fleet.NewRegistry(cfg, fleet.DefaultConnFactory(clock.Real{}))
	reg.Start(ctx)

	// Backend gate for destructive verbs on protected clusters: the frontend
	// ConfirmDialog arms a one-shot grant (ConfigService.ArmProtected) and each
	// destructive service method consumes it. The protection set is read once
	// from the startup config; hot-added clusters are never protected (marking
	// one protected is a fleet.yaml edit + restart, like the rest of the
	// startup config).
	protected := map[string]bool{}
	for _, cc := range cfg.Clusters {
		if cc.Protected {
			protected[cc.Name] = true
		}
	}
	appbridge.SetProtectGuard(appbridge.NewProtectGuard(
		func(name string) bool { return protected[name] },
		time.Now,
	))

	svc := appbridge.NewFleetService(reg, cfg, time.Now)

	em := &emitterAdapter{}
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

	configSvc := appbridge.NewConfigService(configPath(), cfg, func(cc config.ClusterConfig) error {
		return reg.Add(ctx, cc)
	})

	argoSvc := appbridge.NewArgoService(func(name string) (appbridge.ArgoConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	app := application.New(application.Options{
		Name:        "Klyx",
		Description: "Platform-engineer-grade Kubernetes desktop client",
		Services: []application.Service{
			application.NewService(svc),
			application.NewService(gitopsSvc),
			application.NewService(certManagerSvc),
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

	go svc.Run(ctx, em, time.Second)

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "Klyx",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 0,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(15, 20, 30),
		URL:              "/",
	})

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
