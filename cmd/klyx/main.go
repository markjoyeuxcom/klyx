package main

import (
	"context"
	"embed"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/appbridge"
	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/clustermesh"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/execenv"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/secretstore"
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

// claudeBuildsPath records the Claude Code builds that passed Test, beside
// the fleet config like the window state.
func claudeBuildsPath() string {
	return filepath.Join(filepath.Dir(configPath()), "claude-builds.json")
}

func main() {
	// The cache diff's confined build (The Line slice 6 note §7.1): Klyx's
	// own binary in a contained child, before anything else starts.
	if len(os.Args) == 2 && os.Args[1] == cachediff.ChildFlag {
		os.Exit(cachediff.RunChild(os.Stdin, os.Stdout, os.Stderr))
	}
	execenv.ApplyDesktopToolPath()

	cfg, err := config.Load(configPath())
	if err != nil {
		log.Printf("warn: could not load fleet config (%v); starting with empty fleet", err)
		cfg = &config.Config{}
	}
	// The AI eval (eval design §3) runs on the configuration its preflight
	// checked, never on a second read of the file.
	if evalRun != nil {
		cfg = evalRun.Config()
	}
	for _, w := range cfg.Warnings() {
		log.Printf("config warning: %s", w)
	}
	log.Printf("%s", cfg.Summary())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The emitter exists before any cluster starts: each cluster's health
	// Observer (1c) publishes through it from its first evaluation. The app
	// handle is installed atomically once it exists (events before that are
	// dropped; HealthService.Summaries bootstraps the retained summaries).
	em := &appbridge.LateEmitter{}
	// External endpoints' credentials (coverage C5): the OS keychain, and
	// the in-memory revisions Settings > Endpoints moves.
	endpointCreds := fleet.Credentials{Store: secretstore.OS{}, Revisions: endpointcred.NewRegistry()}
	factory := fleet.DefaultConnFactory(clock.Real{}, endpointCreds)
	if evalRun != nil {
		// the connection the preflight resolved, and no other
		factory = fleet.ResolvingConnFactory(clock.Real{}, endpointCreds, evalRun.Resolve)
	}
	reg := fleet.NewRegistry(cfg, factory)
	healthSink := appbridge.NewHealthSink(em)
	reg.SetHealthSink(healthSink)
	// The fleet metadata index (The Line slice 9): each cluster's Observer
	// and connection word reach it from its conn.
	var ixSettings fleetindex.Settings
	if ic := cfg.Index; ic != nil {
		var warnings []string
		ixSettings, warnings = fleetindex.FromConfig(ic.Kinds, ic.Kinds != nil, ic.EntriesPerCluster, ic.MiBPerCluster, ic.EntriesFleet, ic.MiBFleet)
		for _, w := range warnings {
			log.Printf("config warning: %s", w)
		}
	}
	ixSettings.Gaps = fleet.IndexGaps()
	index := fleetindex.New(ixSettings)
	defer index.Stop()
	reg.SetIndex(index)
	// Health notifications (The Line slice 10): the listener is in place
	// before any cluster starts, so every connect's baseline is seen. The
	// cluster lookups resolve once configSvc is published: they run on the
	// Observer loops, which start before it exists.
	notifySvc := notifications.New()
	var liveConfig atomic.Pointer[appbridge.ConfigService]
	// mainWin is assigned after window creation below; the closures are
	// nil-safe until then.
	var mainWin application.Window
	healthNotify := appbridge.NewNotificationsService(configPath(), cfg,
		func(name string) (config.ClusterConfig, bool) {
			cs := liveConfig.Load()
			if cs == nil {
				return config.ClusterConfig{}, false
			}
			return appbridge.LiveClusterConfig(cs)(name)
		},
		func(id, title, body string, data map[string]any) error {
			return notifySvc.SendNotification(notifications.NotificationOptions{ID: id, Title: title, Body: body, Data: data})
		},
		notifySvc.RequestNotificationAuthorization,
		em.Emit)
	healthSink.AddListener(appbridge.NotificationsListener(healthNotify))
	appbridge.RunNotifications(healthNotify)
	defer appbridge.StopNotifications(healthNotify)
	notifySvc.OnNotificationResponse(func(r notifications.NotificationResult) {
		if r.Error != nil || !strings.HasPrefix(r.Response.ID, "klyx-health-") {
			return
		}
		if mainWin != nil {
			mainWin.Show()
			mainWin.Focus()
		}
		appbridge.NotificationClicked(healthNotify, r.Response.UserInfo)
	})
	reg.Start(ctx)

	// configSvc is built before the fleet service: the fleet DTOs resolve each
	// cluster's config (env, group, EFFECTIVE protection) from its live fleet,
	// which includes clusters hot-added through Settings.
	var lineSvc *appbridge.LineService // set below; a hot-add rescans the line at once
	configSvc := appbridge.NewConfigService(configPath(), cfg, func(cc config.ClusterConfig) error {
		err := reg.Add(ctx, cc)
		if lineSvc != nil {
			lineSvc.Rescan()
		}
		return err
	})
	liveConfig.Store(configSvc)

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
	// Bootstraps answer from the rows lineage the sink publishes against.
	appbridge.LinkRowsLineage(healthSvc, healthSink)

	winOpener := &windowOpener{}

	gitopsSvc := appbridge.NewGitOpsService(
		func(name string) (appbridge.GitOpsConn, bool) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, false
			}
			return c, true
		},
		time.Now,
	)

	veleroSvc := appbridge.NewVeleroService(func(name string) (appbridge.VeleroConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	admissionSvc := appbridge.NewAdmissionService(func(name string) (appbridge.AdmissionConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	identitySvc := appbridge.NewIdentityService(func(name string) (appbridge.IdentityConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	// coverage S9: KEDA's on-demand inspector reads, the replica owner and
	// Pause/Resume
	kedaSvc := appbridge.NewKEDAService(func(name string) (appbridge.KEDAConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	certManagerSvc := appbridge.NewCertManagerService(
		func(name string) (appbridge.CertManagerConn, bool) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, false
			}
			return c, true
		},
		time.Now,
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

	meshSvc := appbridge.NewMeshService(func(ctx context.Context) []clustermesh.Member {
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

	// The Line (slice 3): one acquisition point over the Observers' shared
	// Flux and Argo rows, pushed as line:dto.
	var lineOverride *line.Key
	if cfg.Line != nil && cfg.Line.Source != nil {
		k := line.OverrideKey(cfg.Line.Source.URL, cfg.Line.Source.Ref.Kind, cfg.Line.Source.Ref.Name)
		lineOverride = &k
	}
	// The Line slice 7b: each HTTPRoute rule's fingerprint history, the
	// proof a rule's metrics are attributable; 7c attributes them with it.
	appbridge.SetRuleIntervals(gatewaySvc, appbridge.RouteRuleHistory(healthSink).Interval)
	// The Line slice 8d: each Flux unit's applied value, the data path's
	// revision proximity.
	appbridge.SetAppliedChanges(gatewaySvc, appbridge.AppliedHistory(healthSink))

	// The Line slice 6: read-only Git access through the system git, only
	// from fleet.yaml's approved git.repos (Settings > Git); it orders the
	// line against its history after every LineDTO (line:git).
	gitSvc := appbridge.NewGitService(ctx, appbridge.GitDeps{
		ConfigPath: configPath(),
		Override:   lineOverride,
		Emitter:    em,
	})
	// "Compute diff" builds from the Git cache when the repository is approved.
	appbridge.SetGitOpsCacheDiff(gitopsSvc, gitSvc)
	lineSvc = appbridge.NewLineService(ctx, appbridge.LineDeps{
		Emitter:   em,
		Sink:      healthSink,
		Clusters:  appbridge.LiveFleetClusters(configSvc),
		Snapshots: reg.Snapshots,
		Observer: func(name string) *observe.Observer {
			c, ok := reg.Conn(name)
			if !ok {
				return nil
			}
			if h, ok := c.(interface{ Health() *observe.Observer }); ok {
				return h.Health()
			}
			return nil
		},
		Mesh:     meshSvc,
		Override: lineOverride,
		OnLine:   appbridge.GitLineSink(gitSvc),
	})

	// The Line slice 7a: each object's last recorded managed-field update.
	writesSvc := appbridge.NewObjectWritesService(func(name string) (appbridge.ObjectWritesConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	metricsSvc := appbridge.NewMetricsService(func(name string) (appbridge.MetricsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	// Settings > Endpoints (coverage C5): credentials for the external
	// endpoints, fenced against the fleet.yaml the person reviewed.
	endpointsSvc := appbridge.NewEndpointsService(configPath(), endpointCreds.Store, endpointCreds.Revisions, func(name string) (appbridge.EndpointConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		ec, ok := c.(appbridge.EndpointConn)
		return ec, ok
	})

	workloadsSvc := appbridge.NewWorkloadsService(func(name string) (appbridge.WorkloadsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	podsSvc := appbridge.NewPodsService(func(name string) (appbridge.PodsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	})

	logsSvc := appbridge.NewLogsService(func(name string) (appbridge.LogsConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		return c, true
	}, em)

	// coverage S8b: pod log history from the cluster's Loki
	logHistorySvc := appbridge.NewLogHistoryService(appbridge.ReaderOf(func(name string) (appbridge.LokiConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		lc, ok := c.(appbridge.LokiConn)
		return lc, ok
	}))

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

	// AI-assist (read-only Explain and Suggest-fix). Off unless fleet.yaml
	// configures a provider. The snapshot is built once from the startup config:
	// the key comes from exactly the configured source (the OS keychain by
	// default), and a problem leaves AI unavailable with that reason. The egress
	// gate reuses configSvc.IsProtected (live) exactly like the ProtectGuard.
	ai.UseClaudeBuildsFile(claudeBuildsPath())
	aiState := appbridge.BuildAIState(cfg.AI, ai.OSKeyStore{}, os.Getenv)
	if aiState.Problem != "" {
		log.Printf("klyx: AI (%s) unavailable: %s", aiState.ProviderID, aiState.Problem)
	}
	aiSvc := appbridge.NewAIService(
		func(name string) (appbridge.AIConn, bool) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, false
			}
			return c, true
		},
		aiState, configSvc.IsProtected,
		func(name string) capability.Set {
			for _, sn := range reg.Snapshots() {
				if sn.Name == name {
					return sn.Capabilities
				}
			}
			return capability.Set{}
		},
	)
	// The cluster chat (spec 2026-10-14): one conversation per cluster, pinned
	// to the conn the registry holds now. A conn is usable while it is
	// synced, degraded or stale (reads can still work); a failed, connecting
	// or replaced conn ends the conversation.
	chatSvc := appbridge.NewChatService(func(name string) (appbridge.ChatConn, bool) {
		c, ok := reg.Conn(name)
		if !ok {
			return nil, false
		}
		cc, ok := c.(appbridge.ChatConn)
		if !ok {
			return nil, false
		}
		switch c.Snapshot().State {
		case fleet.Synced, fleet.Degraded, fleet.Stale:
			return cc, true
		}
		return cc, false
	}, aiSvc, em)
	appbridge.SetChatGit(chatSvc, gitSvc)               // AI A1 §3.2: git_changes
	appbridge.SetChatLogHistory(chatSvc, logHistorySvc) // AI A1 §3.6: get_log_history

	// Settings > AI writes fleet.yaml's ai block under the shared fleet write
	// lock and swaps aiSvc's snapshot after a successful build and write.
	aiSettingsSvc := appbridge.NewAISettingsService(configPath(), ai.OSKeyStore{}, os.Getenv, aiSvc, cfg.AI)

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
	// The bounds the window was created with (zero on first launch), for
	// winstate.Settle at shutdown.
	var requestedBounds winstate.Bounds
	dockSvc := dock.New()
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

	services := []application.Service{
		application.NewService(svc),
		application.NewService(healthSvc),
		application.NewService(gitopsSvc),
		application.NewService(certManagerSvc),
		application.NewService(veleroSvc),
		application.NewService(admissionSvc),
		application.NewService(identitySvc),
		application.NewService(kedaSvc),
		application.NewService(crdSvc),
		application.NewService(gatewaySvc),
		application.NewService(meshSvc),
		application.NewService(lineSvc),
		application.NewService(healthNotify),
		application.NewService(appbridge.NewIndexService(index)),
		application.NewService(gitSvc),
		application.NewService(writesSvc),
		application.NewService(metricsSvc),
		application.NewService(endpointsSvc),
		application.NewService(workloadsSvc),
		application.NewService(podsSvc),
		application.NewService(logsSvc),
		application.NewService(logHistorySvc),
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
		application.NewService(aiSettingsSvc),
		application.NewService(chatSvc),
		application.NewService(dockSvc),
		application.NewService(notifySvc),
		application.NewService(shellSvc),
	}
	// The AI eval (eval design §1) takes over here, once every service is
	// wired, before any app or window exists. Only the eval build sets it.
	if evalRun != nil {
		os.Exit(evalRun.Run(ctx, evalDeps{chat: chatSvc, conn: func(name string) (appbridge.ChatConn, string) {
			c, ok := reg.Conn(name)
			if !ok {
				return nil, "the lab cluster has no connection"
			}
			cc, ok := c.(appbridge.ChatConn)
			if !ok {
				return nil, "the lab cluster's connection cannot serve chat"
			}
			// the states chat's own lookup accepts, above
			switch st := c.Snapshot().State; st {
			case fleet.Synced, fleet.Degraded, fleet.Stale:
				return cc, ""
			default:
				return nil, "the lab cluster's connection is " + st.String()
			}
		}}))
	}

	// The native smoke (The Line slice 12b) is its own app: its own
	// single-instance key and its bound service, only under the smoke tag.
	uniqueID := "com.moomora.klyx"
	if smoke != nil {
		uniqueID = smoke.UniqueID()
		services = append(services, smoke.Service())
	}

	app := application.New(application.Options{
		Name:        "Klyx",
		Description: "Platform-engineer-grade Kubernetes desktop client",
		// One Klyx per machine: a second instance would double the informer
		// load on every cluster and race the first on fleet.yaml writes. The
		// second launch exits after waking the running instance.
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: uniqueID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				log.Printf("second Klyx launch redirected to this instance")
			},
		},
		Services: services,
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
			// Remember the window geometry for the next launch (best-effort);
			// a smoke run leaves it alone.
			if mainWin != nil && smoke == nil {
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
			eventsSvc.CloseAll()
		},
	})

	em.Install(&emitterAdapter{app: app})
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
	if smoke != nil {
		smoke.Window(&mainOpts)
	} else if b, ok := winstate.Load(windowStatePath()); ok {
		mainOpts.Width, mainOpts.Height = b.Width, b.Height
		mainOpts.X, mainOpts.Y = b.X, b.Y
		mainOpts.InitialPosition = application.WindowXY
		requestedBounds = b
	}
	mainWin = app.Window.NewWithOptions(mainOpts)
	if smoke != nil {
		smoke.Start(app)
	}

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
