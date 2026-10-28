// Package settings is Settings' registry (Settings S1 §2): every setting's
// contract and disposition, written once, for the page, its search and the
// console. Coverage tests derive what must be described from the schema,
// the environment facade, the store facades and the fixed bounds, and fail
// on anything without an entry. The registry never holds a value: the
// backend service that owns a value supplies it, and a credential appears
// only as its state.
package settings

import (
	"fmt"
	"sort"

	"github.com/moomora/klyx/internal/bounds"
	"github.com/moomora/klyx/internal/localstore"
)

// Entry is one setting's contract (redesign §3).
type Entry struct {
	ID          string `json:"id"`
	Section     string `json:"section"`
	Label       string `json:"label"`
	Effect      string `json:"effect"`
	Scope       string `json:"scope"`
	Default     string `json:"default"`
	Validation  string `json:"validation"`
	TakesEffect string `json:"takesEffect"`
	Test        string `json:"test,omitempty"`
	Save        string `json:"save"`
	Disposition string `json:"disposition"`
	Source      string `json:"source"`
}

// The sections, in the rail's order.
const (
	Fleet         = "fleet"
	Observability = "observability"
	AI            = "ai"
	Git           = "git"
	Notifications = "notifications"
	Appearance    = "appearance"
	Data          = "data"
	Diagnostics   = "diagnostics"
)

// Sections is the rail's order.
var Sections = []string{Fleet, Observability, AI, Git, Notifications, Appearance, Data, Diagnostics}

// The dispositions (redesign §3). Effective is a store Klyx keeps where it
// is used (a dragged width, the palette's ranking): shown, not offered.
const (
	Editable  = "editable"
	FileOnly  = "effective-file-only"
	Effective = "effective"
	Deferred  = "deferred"
	Fixed     = "fixed"
)

// Where an entry comes from.
const (
	FromSchema     = "schema"
	FromEnv        = "env"
	FromStore      = "store"
	FromBound      = "bound"
	FromCredential = "credential"
)

// When a change takes effect.
const (
	Now       = "now"
	OnSave    = "on Save"
	Reconnect = "at reconnect"
	Restart   = "at restart"
	OnAction  = "on Test or a credential action"
)

const (
	sourcesSave      = "Review and apply in Observability writes the installation into fleet.yaml's sources block, every other line as written, then puts it in force: its subscribers' alerts are read again. A hand edit takes effect through \"Use the file's values\"."
	subscriptionSave = "Review and apply in Observability writes the cluster's alertmanager block into its entry in fleet.yaml, every other line as written, then puts it in force. A hand edit takes effect through \"Use the file's values\"."
	clusterSave      = "Review and apply in Fleet writes the changed fields into the cluster's entry in fleet.yaml, every other line as written, then uses them. A hand edit of the entry takes effect through \"Use the file's values\"."
	clusterName      = "Edit fleet.yaml: renaming a cluster is a later slice."
	// an endpoint's fields stay file-only until S3
	endpointSave = "Edit fleet.yaml under clusters[]: Klyx reads the clusters when it starts, and Fleet says when the file differs from what runs."
	aiSave       = "Save rewrites fleet.yaml's ai block (comments inside it are not kept) and rebuilds the AI in use."
	gitSave      = "The action rewrites fleet.yaml's git block (comments inside it are not kept) and takes effect at once. A hand edit of the block takes effect through \"Use the file's values\" or at a restart."
	notifySave   = "Each change rewrites fleet.yaml's notifications block at once (comments inside it are not kept). A hand edit of the block takes effect through \"Use the file's values\" or at a restart."
	envSave      = "Set it in the environment Klyx is launched from; a GUI launch may not see shell exports."
	harnessNote  = "A build harness's variable, not a product setting: only a smoke or eval build reads it."
)

// Registry is every entry, sorted by section then id.
func Registry() []Entry {
	var out []Entry
	out = append(out, fleetEntries()...)
	out = append(out, endpointEntries()...)
	out = append(out, sourceEntries()...)
	out = append(out, aiEntries()...)
	out = append(out, gitEntries()...)
	out = append(out, notificationEntries()...)
	out = append(out, indexEntries()...)
	out = append(out, envEntries()...)
	out = append(out, storeEntries()...)
	out = append(out, boundEntries()...)
	rank := map[string]int{}
	for i, s := range Sections {
		rank[s] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Section != out[j].Section {
			return rank[out[i].Section] < rank[out[j].Section]
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func fleetEntries() []Entry {
	// Settings S2: a cluster's fields are edited in Fleet - a context or
	// kubeconfig reconnects - except its name, which stays file-only
	c := func(id, label, effect, def, validation string) Entry {
		e := Entry{ID: "clusters[]." + id, Section: Fleet, Label: label, Effect: effect, Scope: "cluster", Default: def, Validation: validation,
			TakesEffect: OnSave, Save: clusterSave, Disposition: Editable, Source: FromSchema}
		switch id {
		case "name":
			e.TakesEffect, e.Save, e.Disposition = Restart, clusterName, FileOnly
		case "context", "kubeconfig":
			e.TakesEffect = Reconnect
		}
		return e
	}
	line := func(id, label, effect, validation string) Entry {
		return Entry{ID: "line.source." + id, Section: Fleet, Label: label, Effect: effect, Scope: "fleet", Default: "unset: the Git key most stations share",
			Validation: validation, TakesEffect: Restart, Save: "Edit fleet.yaml under line.source.", Disposition: FileOnly, Source: FromSchema}
	}
	return []Entry{
		c("name", "Name", "The cluster's name everywhere in Klyx: the fleet, the location bar, notifications and the console.", "none: required", "Unique in the fleet; not empty."),
		c("context", "Context", "The kubeconfig context Klyx connects through. Changing it connects to whatever that context names.", "none: required", "A context in the cluster's kubeconfig."),
		c("kubeconfig", "Kubeconfig", "The kubeconfig file or path list this cluster's context is read from, and that kubectl, helm and flux are pinned to.", "KUBECONFIG, else ~/.kube/config", "A file, or an OS path list; empty uses the default."),
		c("tags", "Tags", "Free labels shown on the cluster and used for grouping. tags.env is read as the environment when environment is unset; a tag named like a cluster field is warned about, since it sets nothing.", "none", "A map of strings."),
		c("group", "Group", "The group the fleet view places the cluster in.", "none", "Any string; empty is ungrouped."),
		c("environment", "Environment", "The cluster's environment (dev, stg, prd). A production token here makes the cluster protected even without protected: true.", "tags.env, else none", "Any string."),
		c("protected", "Protection", "A protected cluster asks for a typed confirmation before every destructive verb, and keeps its context from an off-machine AI unless protected-cluster egress is allowed. It is also inferred from a prd, prod or production token in the environment, name or context; this flag makes it explicit, and false never removes an inferred protection.", "false (inferred protection still applies)", "true or false."),
		line("url", "Comparison source repository", "Overrides the repository The Line compares stations against. An override that matches no station lights no segment.", "A repository URL, as Flux names it."),
		line("ref.kind", "Comparison source ref kind", "The kind of ref the comparison source names.", "branch, tag, semver, commit or name."),
		line("ref.name", "Comparison source ref", "The ref the comparison source names.", "The ref's name, in Flux's terms."),
	}
}

func endpointEntries() []Entry {
	kinds := []struct{ id, label, found string }{
		{"metrics", "Metrics", "Absent, Klyx discovers Prometheus in the cluster."},
		{"alertmanager", "Alertmanager", "Absent, Klyx discovers Alertmanager in the cluster."},
		{"loki", "Loki", "Loki is never discovered, so its block needs an endpoint or a Service."},
	}
	var out []Entry
	for _, k := range kinds {
		e := func(id, label, effect, def, validation string) Entry {
			return Entry{ID: "clusters[]." + k.id + "." + id, Section: Observability, Label: k.label + " " + label, Effect: effect, Scope: "endpoint", Default: def, Validation: validation,
				TakesEffect: Restart, Test: "Test under External endpoints reads this endpoint and says what answered.", Save: endpointSave, Disposition: FileOnly, Source: FromSchema}
		}
		out = append(out,
			e("endpoint", "URL", "The URL Klyx reads "+k.label+" from. "+k.found, "none", "An http or https URL with no credential in it."),
			e("tlsSkipVerify", "certificate check off", "Accept any certificate this endpoint presents: the connection is encrypted but the server is not authenticated, and External endpoints says so on its row.", "false", "true or false."),
			e("serviceRef.namespace", "Service namespace", "The namespace of the in-cluster Service Klyx reaches "+k.label+" through, by the API server's proxy.", "none", "A namespace name."),
			e("serviceRef.name", "Service name", "The in-cluster Service Klyx reaches "+k.label+" through.", "none", "A Service name."),
			e("serviceRef.port", "Service port", "The Service port, by name or number.", "the Service's first port", "A port name or number."),
			e("serviceRef.scheme", "Service scheme", "Whether the proxy speaks http or https to the Service.", "http", "http or https."),
			e("serviceRef.path", "Service path prefix", "The prefix the Service serves its API under (Mimir's Alertmanager under /alertmanager).", "none", "A path; empty for none."),
			e("auth.type", "authentication", "How Klyx authenticates: a bearer token or a basic username and password. The secret itself is in the keychain, never in the file.", "none", "bearer or basic."),
			e("auth.username", "username", "The basic-authentication username; the password is in the keychain.", "none", "Any string; basic only."),
			e("tenant", "tenant", "Sent as X-Scope-OrgID, for a multi-tenant Mimir, Cortex or Loki.", "none", "Any string; empty sends none."),
		)
		if k.id != "metrics" {
			out = append(out, e("match", "match", "The labels that make an alert or a log stream this cluster's in a shared "+k.label+": sent as the endpoint's filter and checked again on what it returns. None means everything there is this cluster's.", "none", "A map of label equality matchers."))
		} else {
			// Settings S3b §3.3: every metrics query of the cluster carries it
			out = append(out, e("match", "match", "The labels that make a series this cluster's in a shared Prometheus or Mimir tenant: every metrics query Klyx sends for the cluster carries them, in every selector. None means every series there is this cluster's.", "none", "A map of label equality matchers, none of them a label Prometheus reserves (__name__ or another __ prefix)."))
		}
	}
	// Settings S3a §2.1, S3b §2.1: a cluster's subscription to an
	// installation. Loki's and metrics' are file-only in S3b-1: Klyx reads
	// through their installations from S3b-2, and puts a change in force at
	// a restart until Settings edits them.
	sub := func(kind, id, label, effect, def, validation string) Entry {
		name := map[string]string{"alertmanager": "Alertmanager", "loki": "Loki", "metrics": "Metrics"}[kind]
		e := Entry{ID: "clusters[]." + kind + "." + id, Section: Observability, Label: name + " " + label, Effect: effect, Scope: "subscription", Default: def, Validation: validation,
			TakesEffect: OnSave, Save: subscriptionSave, Disposition: Editable, Source: FromSchema}
		if kind != "alertmanager" {
			e.TakesEffect, e.Disposition = Restart, FileOnly
			e.Save = "Edit fleet.yaml: a " + name + " subscription is put in force at a restart in this version, and read through its installation from a later one."
		}
		return e
	}
	for _, kind := range []string{"alertmanager", "loki", "metrics"} {
		noun := map[string]string{"alertmanager": "alerts", "loki": "log history", "metrics": "metrics"}[kind]
		fallback := map[string]string{"alertmanager": "else discovery", "loki": "else none: Loki is never discovered", "metrics": "else discovery"}[kind]
		out = append(out,
			sub(kind, "source", "installation", "Subscribes the cluster to an installation in sources, with this block's tenant and match over the installation's defaults. A cluster's own block always wins over an installation that serves it.", "none: an installation serving the cluster by name or group, "+fallback, "An id in sources, of kind "+kind+"."),
			sub(kind, "scope", "scope", "tenant takes everything in the subscription's tenant as the cluster's own. Allowed only when no other subscription of the installation shares that tenant.", "the member label equal to the cluster's name, and the match", "tenant, with source; no match beside it."),
			sub(kind, "off", "off", "Reads no "+noun+" for the cluster, and says so, even when an installation serves it.", "left out: "+noun+" "+map[bool]string{true: "is", false: "are"}[kind == "loki"]+" read", "true, or left out; nothing else in the block."),
		)
	}
	tok := Entry{ID: "clusters[].metrics.token", Section: Observability, Label: "Metrics token (legacy)", Effect: "A plain-text bearer token, still read. External endpoints moves it to the keychain and replaces this line with auth.", Scope: "endpoint",
		Default: "none", Validation: "Any string.", TakesEffect: Restart, Save: "Move it to the keychain under External endpoints, or edit fleet.yaml.", Disposition: FileOnly, Source: FromSchema}
	cred := Entry{ID: "credential.endpoint", Section: Observability, Label: "Endpoint credential", Effect: "The token or password for an endpoint with auth, kept in the OS keychain for that cluster and endpoint. Klyx shows only whether it is stored.", Scope: "endpoint",
		Default: "none stored", Validation: "Not empty.", TakesEffect: OnAction, Test: "Test reads the endpoint with it.", Save: "Store and Remove change the keychain at once; nothing goes to fleet.yaml.", Disposition: Editable, Source: FromCredential}
	// Settings S3a §5: an installation's own credential, never a cluster's
	srcCred := Entry{ID: "credential.source", Section: Observability, Label: "Installation credential", Effect: "The token or password for an installation read at an endpoint with auth, kept in the OS keychain as source/<id> and bound to the endpoint's origin. Klyx shows only whether it is stored; a cluster's credential is never copied into it.", Scope: "installation",
		Default: "none stored", Validation: "Not empty.", TakesEffect: OnAction, Test: "Test reads the installation's status with it.", Save: "Store and Remove change the keychain at once; nothing goes to fleet.yaml.", Disposition: Editable, Source: FromCredential}
	return append(out, tok, cred, srcCred)
}

func aiEntries() []Entry {
	a := func(id, label, effect, def, validation string) Entry {
		return Entry{ID: "ai." + id, Section: AI, Label: label, Effect: effect, Scope: "fleet", Default: def, Validation: validation, TakesEffect: OnSave,
			Test: "Test sends one short request to the provider in use, with this draft.", Save: aiSave, Disposition: Editable, Source: FromSchema}
	}
	legacy := func(id, label, effect string) Entry {
		e := a(id, label, effect, "none", "Still read; Settings writes the current spelling.")
		e.Disposition, e.Test, e.Save = FileOnly, "", "Saving the AI settings writes the current spelling in its place."
		return e
	}
	return []Entry{
		a("provider", "AI provider", "Which AI Klyx uses for Explain, Suggest-fix, chat and investigations. Off turns every AI surface off.", "off", "off, anthropic, openai, gemini, openaiCompatible or claudeCLI."),
		a("model", "Model", "The model every AI call uses.", "claude-opus-5-5 for Anthropic and the Claude Code CLI; the others must be chosen", "A model the provider serves; Load models lists them."),
		a("keySource", "Key source", "Where Klyx reads the API key: the keychain, an environment variable, or none for a local server.", "keychain", "keychain, env or none; legacy for a key still in fleet.yaml."),
		a("apiKeyEnv", "Environment variable name", "The variable Klyx reads the key from when the key comes from the environment. A GUI launch may not see shell exports.", "none", "A variable name."),
		a("baseUrl", "Server URL or custom origin", "For a local OpenAI-compatible server, its URL; for a vendor, an HTTPS origin the key is sent to instead of the vendor's own.", "the vendor's origin; http://localhost:11434 for a local server", "An http URL for a local server, https otherwise."),
		a("cliPath", "Claude Code path", "The Claude Code binary to run, instead of the one found on PATH or in ~/.local/bin.", "found automatically", "An absolute path to an executable."),
		a("allowProtectedEgress", "Allow protected-cluster egress", "Send protected (prd) clusters' context to the AI in use. One permission for chat, Explain, Suggest-fix and investigations, and it follows whichever provider and origin are in use: it stays on if you change them later. Off: AI stays unavailable on protected clusters unless the model runs on this machine.", "false", "true or false; turning it on asks you to type a confirmation, and saves."),
		a("tlsSkipVerify", "Skip certificate verification", "Insecure, for a local server only: Klyx accepts any certificate this server presents, for any host name, so the connection is encrypted but the server is not authenticated. Use it only for a server on a network you trust; prompts and the key go to whoever answers.", "false", "true or false."),
		a("refusalFallback", "Refusal fallback", "Let Anthropic retry a refused request on another Claude model; the result names the model used.", "true", "true or false; Anthropic only."),
		a("proposeActions", "Propose actions in investigations", "An investigation may propose day-2 steps, each run only when you review and confirm it. Off, the remedy stays text.", "true", "true or false."),
		legacy("backend", "Backend (legacy)", "The pre-2026-10-13 spelling of the provider: hosted is anthropic, local is openaiCompatible."),
		legacy("apiKey", "API key in the file (legacy)", "A plain-text key, still read when the key source is legacy. Move to keychain removes it from the file."),
		{ID: "credential.ai-key", Section: AI, Label: "API key", Effect: "The provider's API key, kept in the OS keychain. Klyx shows only whether one is stored; a typed key is its own draft, never saved with the section.", Scope: "fleet",
			Default: "none stored", Validation: "Not empty; at most 4096 bytes.", TakesEffect: OnAction, Test: "Test uses the stored key.", Save: "Save key and Remove key change the keychain at once.", Disposition: Editable, Source: FromCredential},
	}
}

func gitEntries() []Entry {
	g := func(id, label, effect, def, validation, disp string) Entry {
		return Entry{ID: "git.repos[]." + id, Section: Git, Label: label, Effect: effect, Scope: "fleet", Default: def, Validation: validation,
			TakesEffect: OnSave, Save: gitSave, Disposition: disp, Source: FromSchema}
	}
	rb := g("remoteBases", "Remote Kustomize bases", "Not supported: the cache diff refuses remote bases, so this flag changes nothing.", "false", "true or false.", Deferred)
	rb.Save = "Not offered."
	return []Entry{
		g("url", "Approved URL", "The URL Klyx fetches from, run exactly as approved through your system git, with your SSH agent and credential helper. A URL read from a cluster is only matched to one, never run.", "none", "https or ssh, with no token in it.", Editable),
		g("web", "Open in Git link", "The https base for \"Open in Git\" on a host Klyx does not know.", "none", "An https URL, optional.", Editable),
		g("matches", "Also serves", "Other repository keys this entry serves, such as an SSH host alias the clusters use.", "none", "host/org/repo keys.", Editable),
		rb,
	}
}

func notificationEntries() []Entry {
	n := func(id, label, effect, def, validation string) Entry {
		return Entry{ID: "notifications." + id, Section: Notifications, Label: label, Effect: effect, Scope: "fleet", Default: def, Validation: validation,
			TakesEffect: Now, Save: notifySave, Disposition: Editable, Source: FromSchema}
	}
	return []Entry{
		n("enabled", "Notify me about new faults", "Off until enabled. Faults already present when a cluster connects are listed once in Activity, never notified; nothing is sent while Klyx shows that cluster. Turning it on asks the OS for permission.", "false", "true or false."),
		n("scope", "Clusters", "Which clusters' new faults are notified: protected clusters only, or every cluster.", "protected", "protected or all."),
		n("recoveries", "Also notify recoveries", "A recovery is notified only for a fault you were notified about, and only once it is healthy again.", "false", "true or false."),
	}
}

func indexEntries() []Entry {
	i := func(id, label, effect, def, validation string) Entry {
		return Entry{ID: "index." + id, Section: Data, Label: label, Effect: effect, Scope: "fleet", Default: def, Validation: validation,
			TakesEffect: Restart, Save: "Edit fleet.yaml under index; editable here in a later slice.", Disposition: FileOnly, Source: FromSchema}
	}
	return []Entry{
		i("kinds", "Indexed kinds", "The kinds the fleet metadata index keeps for the console's search.", "the default kinds", "group/resource or group/version/resource entries. Absent keeps the defaults; an empty list indexes nothing."),
		i("entriesPerCluster", "Entries per cluster", "A configured limit on the index's entries for one cluster.", "50,000", "A positive count; zero keeps the default."),
		i("mibPerCluster", "MiB per cluster", "A configured limit on the index's memory for one cluster.", "32 MiB", "MiB; zero keeps the default."),
		i("entriesFleet", "Entries for the fleet", "A configured limit on the index's entries across the fleet.", "200,000", "A positive count; zero keeps the default."),
		i("mibFleet", "MiB for the fleet", "A configured limit on the index's memory across the fleet.", "128 MiB", "MiB; zero keeps the default."),
	}
}

// envText describes each declared environment name; a declaration without
// one fails the environment coverage test.
var envText = map[string][2]string{
	"KLYX_CONFIG":          {"Fleet file location", "Where Klyx reads fleet.yaml, and so where it keeps the window state, tested Claude Code builds and the Git cache beside it."},
	"KLYX_ROWS_TRACE":      {"Rows trace (developer)", "A file the health rows' lineage is traced to, for debugging."},
	"KLYX_LAB_NOISE_BYTES": {"Lab noise (developer)", "Pads lab rows to a size, for load tests."},
	"KLYX_GIT_PATH":        {"git override", "The git binary Klyx runs, instead of the one on PATH. A missing file falls back to PATH, and Diagnostics says so."},
	"KLYX_KUBECTL_PATH":    {"kubectl override", "The kubectl binary Klyx runs for describe, drain and the exec terminal. A missing file falls back to PATH."},
	"KLYX_HELM_PATH":       {"helm override", "The helm binary Klyx runs for release data and rollback. A missing file falls back to PATH."},
	"KLYX_FLUX_PATH":       {"flux override", "The flux binary Klyx runs for flux diff. A missing file falls back to PATH."},
	"KUBECONFIG":           {"KUBECONFIG", "The kubeconfig a cluster without its own kubeconfig reads, through client-go."},
	"TERMINAL":             {"Terminal (Linux)", "The terminal the exec escape hatch opens on Linux, before the known ones."},
	"SSH_AUTH_SOCK":        {"SSH agent", "Passed to git, so an ssh URL authenticates through your agent."},
	"XDG_CONFIG_HOME":      {"XDG config home", "Passed to git, which reads its own configuration there."},
	"HOME":                 {"HOME", "Your home directory, passed to git and the Claude Code CLI."},
	"PATH":                 {"PATH", "Where tools are found. Klyx appends common package-manager locations when it starts, after yours."},
	"USER":                 {"USER", "Passed to git and the Claude Code CLI."},
	"TMPDIR":               {"TMPDIR", "Passed to git and the Claude Code CLI."},
	"ProgramFiles":         {"ProgramFiles (Windows)", "Where Klyx looks for tools on Windows after PATH."},
	"ProgramFiles(x86)":    {"ProgramFiles(x86) (Windows)", "Where Klyx looks for the AWS CLI on Windows."},
	"ProgramData":          {"ProgramData (Windows)", "Where Klyx looks for Chocolatey's helm on Windows."},
	"ChocolateyInstall":    {"ChocolateyInstall (Windows)", "Where Klyx looks for Chocolatey's tools on Windows."},
	"HTTP_PROXY":           {"HTTP_PROXY", "Honoured by the AI clients; the observability transports deliberately do not use it."},
	"HTTPS_PROXY":          {"HTTPS_PROXY", "Honoured by the AI clients; the observability transports deliberately do not use it."},
	"NO_PROXY":             {"NO_PROXY", "Honoured by the AI clients, with the proxies."},
	"http_proxy":           {"http_proxy", "The lower-case form, honoured likewise."},
	"https_proxy":          {"https_proxy", "The lower-case form, honoured likewise."},
	"no_proxy":             {"no_proxy", "The lower-case form, honoured likewise."},
}

var familyText = map[string][2]string{
	"native-tools": {"kubectl, helm and flux environment", "They inherit Klyx's whole environment, so a kubeconfig exec plugin's own variables (AWS_PROFILE and the like) still reach them; Klyx adds only the cluster's KUBECONFIG. Settings never shows the values."},
	"claude-cli":   {"Claude Code CLI environment", "An allowlist built from scratch: HOME, PATH, TMPDIR, LANG, USER, LOGNAME and LC_*, then fixed lockdown values. Nothing else passes."},
	"git-locale":   {"git locale", "LANG and LC_* reach git, with its owner allowlist; nothing else of the locale does."},
}

var purposeText = map[string][2]string{
	"ai-key": {"AI key variable", "When the AI key comes from the environment, Klyx reads the variable ai.apiKeyEnv names, when it builds the AI in use. Settings shows whether it is set, never its value."},
}

func envEntries() []Entry {
	var out []Entry
	for key, t := range envText {
		out = append(out, Entry{ID: "env." + key, Section: Diagnostics, Label: t[0], Effect: t[1], Scope: "process", Default: "unset",
			Validation: "Read when Klyx starts or uses it; never written.", TakesEffect: Restart, Save: envSave, Disposition: FileOnly, Source: FromEnv})
	}
	for _, key := range harnessNames {
		out = append(out, Entry{ID: "env." + key, Section: Diagnostics, Label: key, Effect: harnessNote, Scope: "process", Default: "unset",
			Validation: "Read only by its harness.", TakesEffect: Restart, Save: "Not a product setting.", Disposition: Fixed, Source: FromEnv})
	}
	for id, t := range familyText {
		out = append(out, Entry{ID: "env.family." + id, Section: Diagnostics, Label: t[0], Effect: t[1], Scope: "process", Default: "the launch environment",
			Validation: "Fixed by Klyx.", TakesEffect: Restart, Save: envSave, Disposition: Fixed, Source: FromEnv})
	}
	for id, t := range purposeText {
		out = append(out, Entry{ID: "env.purpose." + id, Section: AI, Label: t[0], Effect: t[1], Scope: "process", Default: "unset",
			Validation: "The variable ai.apiKeyEnv names.", TakesEffect: OnSave, Save: envSave, Disposition: FileOnly, Source: FromEnv})
	}
	return out
}

// harnessNames are the smoke, eval and contract-test variables (§3.1:
// declared, under "not product settings").
var harnessNames = []string{"KLYX_SMOKE_OUT", "KLYX_SMOKE_THEME", "KLYX_SMOKE_WIDTH", "KLYX_SMOKE_HEIGHT", "KLYX_SMOKE_CLUSTER",
	"KLYX_EVAL_OUT", "KLYX_EVAL_SCENARIOS", "KLYX_EVAL_A3", "KLYX_EVAL_LAB", "KLYX_EVAL_KIND", "KLYX_EVAL_RUNS", "KLYX_CLAUDE_BIN", "KLYX_CLAUDE_CONTRACT"}

func storeEntries() []Entry {
	native := func(name, label, effect string) Entry {
		return Entry{ID: "native." + name, Section: Data, Label: label, Effect: effect, Scope: "process", Default: "beside fleet.yaml",
			Validation: "Kept by Klyx.", TakesEffect: Now, Save: "Klyx writes it; delete actions come in a later slice.", Disposition: Effective, Source: FromStore}
	}
	web := func(key, section, label, effect, def, disp string) Entry {
		save := "Kept in this window's storage where you use it."
		if disp == Editable {
			save = "Chosen here; kept in this window's storage at once."
		}
		return Entry{ID: "store." + key, Section: section, Label: label, Effect: effect, Scope: "webview", Default: def,
			Validation: "Best-effort: unavailable storage keeps the default.", TakesEffect: Now, Save: save, Disposition: disp, Source: FromStore}
	}
	return []Entry{
		native(localstore.Window.Name(), "Window state", "The window's size and place, restored at launch."),
		native(localstore.ClaudeBuilds.Name(), "Tested Claude Code builds", "The Claude Code builds Test admitted on this machine; a build that breaks the contract is dropped."),
		native(localstore.GitCache.Name(), "Git cache", "One bare partial clone per approved repository, and the generated git config, under the fixed caps."),
		web("klyx-theme", Appearance, "Theme", "The app's look: night, day, or the operating system's.", "night", Editable),
		web("klyx.layout.v1.zoom", Appearance, "Zoom", "Scales the whole layout, text and controls, in every Klyx window.", "100%", Editable),
		web("klyx-theme-bench-migrated", Appearance, "Theme migration (Signal Bench)", "Marks the one-time move to the bench themes as done.", "unset", Effective),
		web("klyx-theme-line-migrated", Appearance, "Theme migration (The Line)", "Marks the one-time move to night and day as done.", "unset", Effective),
		web("klyx.layout.v1.inspectorWidth", Appearance, "Inspector width", "The inspector's width, set by dragging its edge.", "the layout's default", Effective),
		web("klyx.layout.v1.lensIndex", Appearance, "Lens index open", "Whether the lens index is open; mod+\\ toggles it.", "open", Effective),
		web("klyx-logs-dock-height", Appearance, "Logs dock height", "The logs dock's height, set by dragging.", "the layout's default", Effective),
		web("klyx.yaml.wrap", Appearance, "YAML wrap", "Whether YAML blocks wrap long lines.", "no wrap", Effective),
		web("klyx-route", Data, "Last place", "Where Klyx was, restored once at launch.", "the fleet", Effective),
		web("klyx-view-prefs", Data, "View preferences", "Each cluster's namespace and structural toggles.", "none", Effective),
		web("klyx-view-search", Data, "View searches", "The lenses' search text, kept for this session only.", "none", Effective),
		web("klyx-palette-frecency", Data, "Console ranking", "How often and how recently you ran each console command, to rank them.", "none", Effective),
		web("klyx-action-log", Data, "Activity", "The persisted Activity log of day-2 actions and their outcomes.", "empty", Effective),
	}
}

func boundEntries() []Entry {
	var out []Entry
	for _, b := range bounds.List() {
		out = append(out, Entry{ID: "bound." + b.ID, Section: Diagnostics, Label: boundLabels[b.ID], Effect: "A fixed safety ceiling: listed, never raised.", Scope: "process",
			Default: fmt.Sprintf("%d %s", b.Value, b.Unit), Validation: "Fixed.", TakesEffect: Restart, Save: "Not offered.", Disposition: Fixed, Source: FromBound})
		if out[len(out)-1].Label == "" {
			out[len(out)-1].Label = b.ID // the coverage test fails on it
		}
	}
	return out
}

var boundLabels = map[string]string{
	"chat.turnDeadline":           "Chat turn deadline",
	"chat.modelCalls":             "Chat model calls per turn",
	"chat.toolCalls":              "Chat tool calls per turn",
	"chat.questionBytes":          "Chat question size",
	"chat.answerBytes":            "Chat answer size",
	"chat.requestBytes":           "Chat request budget",
	"chat.snapshotBytes":          "Chat snapshot size",
	"investigation.turnDeadline":  "Investigation turn deadline",
	"investigation.modelCalls":    "Investigation model calls per turn",
	"investigation.toolCalls":     "Investigation tool calls per turn",
	"investigation.snapshotBytes": "Investigation snapshot size",
	"conversations":               "Conversations at once (chats and investigations)",
	"git.repoBytes":               "Git cache per repository",
	"git.totalBytes":              "Git cache in total",
	"git.contentBytes":            "Git content read at once",
	"rows.roleCap":                "Health rows per role",
	"alerts.perRead":              "Alerts per Alertmanager read",
	"alerts.bytes":                "Alertmanager response size",
	"alerts.appearances":          "Alert group appearances per read",
}

// sourceEntries are the installations (Settings S3a §2.1): what can be
// reached, written once under sources. File-only until Settings applies
// them in S3a's last stage.
func sourceEntries() []Entry {
	e := func(id, label, effect, def, validation string) Entry {
		return Entry{ID: "sources[]." + id, Section: Observability, Label: label, Effect: effect, Scope: "installation", Default: def, Validation: validation,
			TakesEffect: OnSave, Save: sourcesSave, Disposition: Editable, Source: FromSchema}
	}
	return []Entry{
		e("id", "Installation id", "Names the installation: subscriptions, its keychain credential and every read of its alerts carry it.", "none: required", "A DNS label, unique among sources."),
		e("kind", "Installation kind", "What the installation serves: alerts, log history or metrics. Klyx reads through Loki and metrics installations from a later version, and puts them in force at a restart until then.", "none: required", "alertmanager, loki or metrics; discover: true is not for loki."),
		e("name", "Installation name", "A display name, for Settings and the scope sentence.", "the id", "Any string without control characters."),
		e("host", "Host cluster", "The fleet cluster whose API server reaches a serviceRef or discovered installation. It is transport only: it grants no reads of the host's objects, and a host cannot be removed while an installation names it.", "none: required with serviceRef or discover", "A cluster of the fleet; not with endpoint."),
		e("serviceRef.namespace", "Installation Service namespace", "The namespace of the Service, in the host, that the installation is reached through.", "none", "A namespace name."),
		e("serviceRef.name", "Installation Service name", "The Service, in the host, that the installation is reached through.", "none", "A Service name."),
		e("serviceRef.port", "Installation Service port", "The Service port, by name or number.", "none: required with serviceRef", "A port name or number."),
		e("serviceRef.scheme", "Installation Service scheme", "Whether the host's proxy speaks http or https to the Service.", "http", "http or https."),
		e("serviceRef.path", "Installation Service path prefix", "The prefix the Service serves its API under (Mimir's Alertmanager under /alertmanager).", "none", "A path; empty for none."),
		e("discover", "Discover in the host", "Finds the installation in the host with Klyx's discovery and its proof of one installation.", "false", "true or false; exactly one of serviceRef, discover and endpoint."),
		e("endpoint", "Installation URL", "The URL the installation is read from directly, with no host.", "none", "An http or https URL with no credential in it."),
		e("tlsSkipVerify", "Installation certificate check off", "Accept any certificate the endpoint presents: encrypted, but the server is not authenticated.", "false", "true or false; endpoint only."),
		e("auth.type", "Installation authentication", "How Klyx authenticates to the endpoint. The secret is in the keychain as source/<id>, never in the file.", "none", "bearer or basic; endpoint only."),
		e("auth.username", "Installation username", "The basic-authentication username; the password is in the keychain.", "none", "Any string; basic only."),
		e("tenant", "Installation tenant", "The default tenant of its subscriptions, sent as X-Scope-OrgID.", "none", "Any string without control characters."),
		e("memberLabel", "Member label", "The default scope of every subscription: this label equal to the member cluster's name. Subscriptions sharing a tenant need it, with distinct values.", "none: then each subscription states a match or scope: tenant", "A label name; for metrics, not one Prometheus reserves (__name__ or another __ prefix)."),
		e("serves[].group", "Serves a group", "Every cluster of this group without its own alertmanager block subscribes to the installation.", "none", "A group name; one key per entry."),
		e("serves[].cluster", "Serves a cluster", "This cluster, without its own alertmanager block, subscribes to the installation, before any group installation.", "none", "A cluster name; one key per entry."),
	}
}
