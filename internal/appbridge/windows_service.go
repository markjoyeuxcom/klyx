package appbridge

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// WindowOpener abstracts native window creation so WindowsService is testable
// without a running Wails app. main.go supplies the real implementation backed
// by app.Window.NewWithOptions; tests supply a fake that records calls.
type WindowOpener interface {
	OpenWindow(title, url string, width, height int)
}

const (
	// logsWindowWidth / logsWindowHeight size a pop-out log window. Wide enough
	// for unwrapped log lines, tall enough for a useful tail.
	logsWindowWidth  = 980
	logsWindowHeight = 560
)

// WindowsService opens native auxiliary windows (currently: pop-out log tails).
// It is the first multi-window surface in Klyx. Window creation is delegated to
// a WindowOpener so the URL/title/size logic stays unit-testable.
type WindowsService struct {
	opener WindowOpener
}

// NewWindowsService wires the window opener.
func NewWindowsService(opener WindowOpener) *WindowsService {
	return &WindowsService{opener: opener}
}

// OpenLogsWindow opens a native window that tails one container's logs. The new
// window boots the SAME SPA with a `logswin=1` query flag, which the frontend
// detects to render only the log pane (no fleet subscriptions, no sidebar). The
// popout opens its own log stream and closes it best-effort on window unload;
// the per-stream cap and the app-quit CloseAll drain are the backstops if that
// best-effort close is missed.
//
// Validation: cluster, namespace and pod must be non-empty. container may be
// empty (the pane then picks the pod's first container) but the dock always
// passes the currently-selected one.
func (s *WindowsService) OpenLogsWindow(cluster, namespace, pod, container string) ActionResultDTO {
	if cluster == "" || namespace == "" || pod == "" {
		return ActionResultDTO{Error: "cluster, namespace and pod are required"}
	}

	q := url.Values{}
	q.Set("logswin", "1")
	q.Set("cluster", cluster)
	q.Set("ns", namespace)
	q.Set("pod", pod)
	q.Set("container", container)

	target := "/?" + q.Encode()
	title := fmt.Sprintf("logs · %s/%s", namespace, pod)

	s.opener.OpenWindow(title, target, logsWindowWidth, logsWindowHeight)
	return ActionResultDTO{OK: true}
}

// OpenLogHistoryWindow opens a native window on a pod's log history from the
// cluster's Loki (coverage S8b §7), pinned to the pod's UID as the dock's
// History is: mode=history with the UID, created time and whether it is gone.
func (s *WindowsService) OpenLogHistoryWindow(cluster, namespace, pod, uid string, createdUnix int64, gone bool) ActionResultDTO {
	if cluster == "" || namespace == "" || pod == "" || uid == "" {
		return ActionResultDTO{Error: "cluster, namespace, pod and uid are required"}
	}
	q := url.Values{}
	q.Set("logswin", "1")
	q.Set("mode", "history")
	q.Set("cluster", cluster)
	q.Set("ns", namespace)
	q.Set("pod", pod)
	q.Set("uid", uid)
	if createdUnix > 0 {
		q.Set("created", strconv.FormatInt(createdUnix, 10))
	}
	if gone {
		q.Set("gone", "1")
	}
	s.opener.OpenWindow(fmt.Sprintf("log history · %s/%s", namespace, pod), "/?"+q.Encode(), logsWindowWidth, logsWindowHeight)
	return ActionResultDTO{OK: true}
}

// OpenWorkloadLogsWindow opens a native window tailing the AGGREGATE logs of a
// workload (all its pods, fan-in with pod prefixes - see OpenWorkloadLogStream).
// Same boot mechanism as OpenLogsWindow with mode=workload; the frontend passes
// the workload target to the log pane, which opens the aggregate stream.
func (s *WindowsService) OpenWorkloadLogsWindow(cluster, namespace, kind, name, container string) ActionResultDTO {
	if cluster == "" || namespace == "" || kind == "" || name == "" {
		return ActionResultDTO{Error: "cluster, namespace, kind and name are required"}
	}

	q := url.Values{}
	q.Set("logswin", "1")
	q.Set("mode", "workload")
	q.Set("cluster", cluster)
	q.Set("ns", namespace)
	q.Set("kind", kind)
	q.Set("name", name)
	q.Set("container", container)

	target := "/?" + q.Encode()
	title := fmt.Sprintf("logs · %s/%s (%s)", namespace, name, strings.ToLower(kind))

	s.opener.OpenWindow(title, target, logsWindowWidth, logsWindowHeight)
	return ActionResultDTO{OK: true}
}
