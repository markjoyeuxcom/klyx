package appbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"os/exec"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/urlscrub"
)

// ExecConn is the per-cluster surface ExecService needs (lookup seam).
type ExecConn interface {
	ExecCommand(namespace, pod, container string) ([]string, error)
	DebugCommand(namespace, pod, container string, o fleet.DebugOptions) ([]string, error)
	DebugKubectlCheck(ctx context.Context) fleet.DebugKubectl
	CheckPodArmed(ctx context.Context, namespace, name, uid string) error
	DescribeCmd(ctx context.Context, kindArg, namespace, name string) (*exec.Cmd, error)
	DescribeArgv(kindArg, namespace, name string, events bool) ([]string, error)
	// DescribeTarget resolves the resource argument through discovery, and
	// DescribeSnapshot reads the object for the version bracket around
	// kubectl's own read (coverage S0b §4, revision 4).
	DescribeTarget(ctx context.Context, kindArg string) (schema.GroupVersionResource, schema.GroupVersionKind, error)
	DescribeSnapshot(ctx context.Context, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, ns, name string) (uid, resourceVersion, hold string, err error)
	// DescribePinnedCmd is the bracketed describe: the resolved
	// resource.version.group, against a kubeconfig snapshot verified to be
	// the bracket's own connection. cleanup removes the snapshot.
	DescribePinnedCmd(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, events bool) (cmd *exec.Cmd, cleanup func(), err error)
}

// ExecCommandDTO is the response from GetExecCommand.
type ExecCommandDTO struct {
	// Command is the display/copy string: argv joined with shell quoting applied
	// to any argument that contains shell-special characters.
	Command string `json:"command"`
	// Argv is the raw unquoted argument vector, suitable for os/exec.Command.
	Argv  []string `json:"argv"`
	Error string   `json:"error"`
}

// ExecService is bound to JS - the kubectl escape-hatch verbs:
//   - GetExecCommand: returns the kubectl exec argv and a display/copy command string.
//   - OpenExecTerminal: launches the OS terminal running the kubectl exec command.
//   - Describe: runs kubectl describe and returns the output (read-only).
type ExecService struct {
	lookup func(string) (ExecConn, bool)
}

// NewExecService creates an ExecService with the given cluster-lookup function.
func NewExecService(lookup func(string) (ExecConn, bool)) *ExecService {
	return &ExecService{lookup: lookup}
}

// GetExecCommand returns the kubectl exec argv and the copyable command for the
// host's default shell (commandDTO) for the given cluster/namespace/pod/
// container. container may be "" (omitted).
func (s *ExecService) GetExecCommand(cluster, ns, pod, container string) ExecCommandDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ExecCommandDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.ExecCommand(ns, pod, container)
	if err != nil {
		return ExecCommandDTO{Error: err.Error()}
	}
	return commandDTO(argv)
}

// commandDTO pairs argv with the copyable command for the host's default
// shell (displayCommand: PowerShell on Windows, POSIX elsewhere).
func commandDTO(argv []string) ExecCommandDTO {
	cmd, err := displayCommand(hostGOOS, argv)
	if err != nil {
		return ExecCommandDTO{Error: err.Error()}
	}
	return ExecCommandDTO{Command: cmd, Argv: argv}
}

// OpenExecTerminal launches the OS terminal with the kubectl exec command -
// Terminal.app on macOS, the user's emulator ($TERMINAL / x-terminal-emulator /
// the common set) on Linux, PowerShell in a new console on Windows. When
// no terminal can be found the error says to use the copy-command affordance.
func (s *ExecService) OpenExecTerminal(cluster, ns, pod, container string) ActionResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.ExecCommand(ns, pod, container)
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}

	return launchTerminal(argv)
}

// DebugProfileDTO is one offered debug profile and what it adds.
type DebugProfileDTO struct {
	Name string `json:"name"`
	Adds string `json:"adds"`
}

// DebugPreflightDTO is what the debug dialog shows before the confirmation
// (coverage S6 §6): the profiles, the default image, and the kubectl the
// terminal runs - whether it may launch, and any qualification.
type DebugPreflightDTO struct {
	Profiles       []DebugProfileDTO `json:"profiles"`
	DefaultImage   string            `json:"defaultImage"`
	DefaultProfile string            `json:"defaultProfile"`
	Kubectl        string            `json:"kubectl,omitempty"` // its gitVersion
	OK             bool              `json:"ok"`
	Reason         string            `json:"reason,omitempty"`
	Qualified      string            `json:"qualified,omitempty"`
	Error          string            `json:"error,omitempty"`
}

var debugProfileOrder = []string{"general", "baseline", "restricted", "netadmin", "sysadmin"}

// DebugPreflight reads the kubectl gate for the debug dialog. It runs
// `kubectl version --client` only; nothing touches the cluster.
func (s *ExecService) DebugPreflight(cluster string) DebugPreflightDTO {
	out := DebugPreflightDTO{DefaultImage: fleet.DefaultDebugImage, DefaultProfile: fleet.DefaultDebugProfile}
	for _, name := range debugProfileOrder {
		out.Profiles = append(out.Profiles, DebugProfileDTO{Name: name, Adds: fleet.DebugProfiles[name]})
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		out.Error = "cluster not connected: " + cluster
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	k := conn.DebugKubectlCheck(ctx)
	out.Kubectl, out.OK, out.Reason, out.Qualified = k.Version, k.OK, k.Reason, k.Qualified
	return out
}

// GetDebugCommand returns the kubectl debug argv (an ephemeral container
// with the chosen image and an explicit profile, process-namespace-targeted
// at the given container where the runtime supports it) and its display
// string - the escape hatch for distroless images where exec has no shell.
// Copying runs nothing, so it is ungated.
func (s *ExecService) GetDebugCommand(cluster, ns, pod, container, image, profile string) ExecCommandDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return ExecCommandDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.DebugCommand(ns, pod, container, fleet.DebugOptions{Image: image, Profile: profile})
	if err != nil {
		return ExecCommandDTO{Error: err.Error()}
	}
	return commandDTO(argv)
}

// OpenDebugTerminal launches the OS terminal running kubectl debug against the
// pod. Same platform behavior as OpenExecTerminal.
//
// Unlike exec, kubectl debug MUTATES the pod: it adds an ephemeral container
// to the pod spec that Kubernetes cannot remove until the pod is recreated. So
// it consumes the protected-cluster grant like every other mutating verb - the
// frontend arms it through the shared ConfirmDialog. GetDebugCommand (copy the
// command) stays ungated: copying runs nothing.
//
// uid is the pod object the debug shell was armed against (design §5b): it is
// confirmed by a GET before the terminal launches, and a replaced or missing
// pod launches nothing. kubectl debug takes a name, so a same-name
// replacement landing between that check and kubectl's own request is the
// residual window (a StatefulSet's recreated pod-0, say).
//
// The kubectl the terminal runs is checked first (coverage S6 §6): an
// unresolved, unreadable or pre-1.30 kubectl launches nothing.
func (s *ExecService) OpenDebugTerminal(cluster, ns, pod, uid, container, image, profile string) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	argv, err := conn.DebugCommand(ns, pod, container, fleet.DebugOptions{Image: image, Profile: profile})
	if err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if k := conn.DebugKubectlCheck(ctx); !k.OK {
		return ActionResultDTO{Error: "debug refused: " + k.Reason + " - nothing was started"}
	}
	if err := conn.CheckPodArmed(ctx, ns, pod, uid); err != nil {
		return ActionResultDTO{Error: "debug " + err.Error() + " - nothing was started"}
	}
	return launchTerminal(argv)
}

// shellQuoteArgv joins argv into a single string, single-quoting any argument
// that is not provably safe to leave bare.
func shellQuoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = shellQuoteArg(arg)
	}
	return strings.Join(parts, " ")
}

// shellSafe reports whether every rune of arg is in the conservative allowlist
// of characters that never need quoting in a POSIX shell. An allowlist, not a
// special-character denylist: a denylist that forgets one character (newline,
// backslash, ~) turns into a command-splitting bug; an allowlist fails closed.
func shellSafe(arg string) bool {
	if arg == "" {
		return false // an empty argument only survives as ''
	}
	for _, ch := range arg {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		case strings.ContainsRune("@%_+=:,./-", ch):
		default:
			return false
		}
	}
	return true
}

func shellQuoteArg(arg string) string {
	if shellSafe(arg) {
		return arg
	}
	// Single-quote the whole argument; escape embedded single quotes as '\''.
	// Newlines and every other character are literal inside single quotes.
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// DescribeResultDTO is the outcome of a kubectl describe run. Output is
// kubectl's stdout exactly as printed - never trimmed - possibly partial on
// a failed run (The Line slice 14, review 1). Error is set when the run could
// not happen or exited non-zero. Command is the same describe as a command
// for the person's terminal, with the cluster's kubeconfig pinned in it;
// CommandError says why there is none (a Windows kubeconfig path list).
type DescribeResultDTO struct {
	OK           bool   `json:"ok"`
	Output       string `json:"output"`
	Error        string `json:"error"`
	Command      string `json:"command"`
	CommandError string `json:"commandError"`
	// Withheld, when set, is why Describe was not run for this object
	// (coverage S0b §4): a registered payload kind, or a resource that could
	// not be classified. No stdout or stderr is returned with it.
	Withheld string `json:"withheld,omitempty"`
	// Scrubbed: user info was removed from URLs in the output (or lines
	// withheld); the text is no longer kubectl's exact output.
	Scrubbed      bool `json:"scrubbed,omitempty"`
	LinesWithheld int  `json:"linesWithheld,omitempty"`
}

// describeTimeout bounds one kubectl describe run. Same 30s budget as the
// other request/response verbs.
const describeTimeout = 30 * time.Second

// Describe runs `kubectl describe <kindArg> <name>` against the cluster's
// kubeconfig context and returns the raw output. Read-only: describe never
// mutates, so no protected-cluster gate applies. kindArg is "pod"/"node" for
// the core panels and plural.group or singular.group for typed objects; the
// fleet layer validates every argument and resolves the kubectl binary, so a
// missing kubectl comes back as an actionable Error - never as silently
// empty output. ctx is the call's: the sheet cancels a run it no longer
// shows (a closed sheet, another object), which stops kubectl.
func (s *ExecService) Describe(ctx context.Context, cluster, kindArg, namespace, name string) DescribeResultDTO {
	return scrubDescribe(s.describe(ctx, cluster, kindArg, namespace, name))
}

// scrubDescribe applies the URL rule to every text a describe returns, the
// early refusals and errors included (Codex's S0b re-review, finding 5):
// discovery, snapshot, pinning and command errors can quote a URL. The
// output is scrubbed where it is read; a change here is labelled the same.
func scrubDescribe(r DescribeResultDTO) DescribeResultDTO {
	for _, f := range []*string{&r.Error, &r.CommandError, &r.Withheld, &r.Command} {
		if t := urlscrub.Text(*f); t.Changed() {
			*f, r.Scrubbed = t.Text, true
			r.LinesWithheld += t.Withheld
		}
	}
	return r
}

func (s *ExecService) describe(ctx context.Context, cluster, kindArg, namespace, name string) DescribeResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return DescribeResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	// Coverage S0b §4 (revision 4): classify before anything runs. A
	// resource that cannot be classified is withheld. A registered payload
	// kind is described only inside a version bracket: the object is read
	// before and after kubectl, and the report is released only when both
	// reads show the same UID and resourceVersion with no payload - every
	// write changes the resourceVersion, so kubectl's read in between saw
	// that payload-free version. Anything else discards both streams.
	//
	// A message-sensitive kind (S1b §6) is bracketed the same way, and runs
	// without events: kubectl prints condition messages raw and appends the
	// object's events, so its report is released only for a version whose
	// condition messages pass the kind's rule unchanged, and its events are
	// read through Klyx's own surface under that rule.
	gvr, gvk, cerr := conn.DescribeTarget(ctx, kindArg)
	withheld := ""
	payloadKind := cerr == nil && (crd.IsPayloadKind(gvk.Group, gvk.Kind) || crd.MessageBracketed(gvk.Group, gvk.Kind))
	events := cerr != nil || !crd.MessageBracketed(gvk.Group, gvk.Kind)
	var preUID, preRV string
	switch {
	case cerr != nil:
		withheld = "Describe is withheld: Klyx could not tell what kind of object this is (" + cerr.Error() + "), so it cannot rule out templates, patches or values that can carry secrets."
	case payloadKind:
		uid, rv, hold, err := conn.DescribeSnapshot(ctx, gvr, gvk, namespace, name)
		switch {
		case err != nil:
			withheld = "Describe is withheld: Klyx could not read this " + gvk.Kind + " to check it for templates, patches or values that can carry secrets (" + err.Error() + ")."
		case hold != "":
			withheld = holdWithheld(gvk.Kind, hold)
		case uid == "" || rv == "":
			withheld = "Describe is withheld: the server did not report this " + gvk.Kind + "'s identity and version, so Klyx cannot bracket kubectl's read."
		default:
			preUID, preRV = uid, rv
		}
	}
	var res DescribeResultDTO
	if argv, err := conn.DescribeArgv(kindArg, namespace, name, events); err != nil {
		res.CommandError = err.Error()
	} else if line, err := displayCommand(hostGOOS, argv); err != nil {
		res.CommandError = err.Error()
	} else {
		res.Command = line
	}
	if withheld != "" {
		// the copy command stays: the user may run kubectl themselves,
		// knowingly; Klyx does not
		res.Withheld = withheld
		return res
	}
	// A bracketed describe runs exactly what the bracket checks (Codex's S0b
	// implementation review, finding 4): kubectl gets the resolved
	// resource.version.group and a kubeconfig snapshot proven to be this
	// connection. A snapshot that cannot be proven withholds.
	var cmd *exec.Cmd
	var err error
	if payloadKind {
		var cleanup func()
		cmd, cleanup, err = conn.DescribePinnedCmd(ctx, gvr, namespace, name, events)
		if err != nil {
			res.Withheld = "Describe is withheld: Klyx could not bind kubectl to the connection it checks this " + gvk.Kind + " through (" + err.Error() + ")."
			return res
		}
		defer cleanup()
	} else {
		cmd, err = conn.DescribeCmd(ctx, kindArg, namespace, name)
	}
	if err != nil {
		return DescribeResultDTO{Error: err.Error(), Command: res.Command, CommandError: res.CommandError}
	}
	// Stdout and stderr stay separate: describe writes the report to stdout and
	// real errors to stderr. Combining them would let a deprecation warning
	// corrupt the report text.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if payloadKind {
		uid, rv, hold, err := conn.DescribeSnapshot(ctx, gvr, gvk, namespace, name)
		if err != nil || uid == "" || uid != preUID || rv != preRV || hold != "" {
			// discard both streams: the version kubectl read is not proven
			stdout.Reset()
			stderr.Reset()
			if hold != "" {
				res.Withheld = holdWithheld(gvk.Kind, hold)
			} else {
				res.Withheld = "Describe is withheld: this " + gvk.Kind + " changed while it was being described, so Klyx cannot prove the report carries no templates, patches or values. Try again."
			}
			return res
		}
	}
	// Coverage S0b §4: both streams pass the URL text policy before anything
	// is returned; a transformed report is labelled, never called exact.
	out := urlscrub.Text(stdout.String())
	errText := urlscrub.Text(stderr.String())
	res.Output = out.Text
	res.Scrubbed = out.Changed() || errText.Changed()
	res.LinesWithheld = out.Withheld + errText.Withheld
	if runErr != nil {
		msg := strings.TrimSpace(errText.Text)
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			msg = fmt.Sprintf("kubectl describe timed out after %s", describeTimeout)
		case errors.Is(ctx.Err(), context.Canceled):
			msg = "kubectl describe was cancelled"
		case msg == "":
			msg = runErr.Error()
		}
		res.Error = msg
		return res
	}
	res.OK = true
	return res
}

// OpenTerminal opens a plain OS terminal window (no command) - the sidebar's
// external-terminal escape hatch. Same platform behavior as OpenExecTerminal.
func (s *ExecService) OpenTerminal() ActionResultDTO {
	return launchTerminal(nil)
}

// holdWithheld is the refusal for a snapshot's hold.
func holdWithheld(kind, hold string) string {
	switch {
	case hold == crd.HoldMessages && kind == "Event":
		return "Describe is withheld for this Event: its message is about an object whose messages Klyx shows only under that object's rules, and kubectl would print it raw. The inspector shows it under them."
	case hold == crd.HoldMessages && crd.OriginsKind(kind):
		return "Describe is withheld for this " + kind + ": it holds a URL whose path Klyx does not show (a signed artifact URL can carry a token), and kubectl would print it raw. The inspector shows it under Klyx's rules."
	case hold == crd.HoldMessages:
		return "Describe is withheld for this " + kind + ": a condition message is outside Klyx's message rules, and kubectl would print it raw. The inspector shows it under them."
	}
	return payloadWithheld(kind)
}

func payloadWithheld(kind string) string {
	return "Describe is withheld for this " + kind + ": it carries templates, patches or values that can carry secrets, and kubectl's text would include them. The Manifest view shows a sanitised copy."
}
