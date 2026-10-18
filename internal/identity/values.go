// Package identity reads workload identity as evidence (coverage S5): what
// a ServiceAccount declares, what each pod was given, current against
// current, the node and webhook side (C2), and who an External Secrets
// store authenticates as. Pure: parsing and rules over objects the caller
// read, with no client. It never reads a token, a Secret's value, or an env
// variable outside the named identity set, and a named variable's value is
// shape-validated before it is shown (S5 §7).
package identity

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/moomora/klyx/internal/urlscrub"
)

// The named identity variables (S5 §3.2, §7): the only env Klyx keeps.
const (
	EnvRoleARN       = "AWS_ROLE_ARN"
	EnvWebTokenFile  = "AWS_WEB_IDENTITY_TOKEN_FILE"
	EnvCredsURI      = "AWS_CONTAINER_CREDENTIALS_FULL_URI"
	EnvCredsToken    = "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"
	EnvRegion        = "AWS_REGION"
	EnvDefaultRegion = "AWS_DEFAULT_REGION"
	EnvSTSRegional   = "AWS_STS_REGIONAL_ENDPOINTS"
	EnvAzureClient   = "AZURE_CLIENT_ID"
	EnvAzureTenant   = "AZURE_TENANT_ID"
	EnvAzureToken    = "AZURE_FEDERATED_TOKEN_FILE"
	EnvAzureAuthHost = "AZURE_AUTHORITY_HOST"
)

// identityEnv is the allowlist.
var identityEnv = map[string]bool{EnvRoleARN: true, EnvWebTokenFile: true, EnvCredsURI: true, EnvCredsToken: true, EnvRegion: true,
	EnvDefaultRegion: true, EnvSTSRegional: true, EnvAzureClient: true, EnvAzureTenant: true, EnvAzureToken: true, EnvAzureAuthHost: true}

// The ServiceAccount annotations and pod labels and annotations read.
const (
	AnnRoleARN        = "eks.amazonaws.com/role-arn"
	AnnAudience       = "eks.amazonaws.com/audience"
	AnnSTSRegional    = "eks.amazonaws.com/sts-regional-endpoints"
	AnnTokenExpiry    = "eks.amazonaws.com/token-expiration"
	AnnAWSSkip        = "eks.amazonaws.com/skip-containers"
	LabelAWSOptOut    = "eks.amazonaws.com/skip-pod-identity-webhook"
	LabelPodIdentity  = "eks.amazonaws.com/pod-identity"
	AnnAzureClient    = "azure.workload.identity/client-id"
	AnnAzureTenant    = "azure.workload.identity/tenant-id"
	AnnAzureExpiry    = "azure.workload.identity/service-account-token-expiration"
	AnnAzureSkip      = "azure.workload.identity/skip-containers"
	LabelAzureUse     = "azure.workload.identity/use"
	AnnGKEServiceAcct = "iam.gke.io/gcp-service-account"
	LabelComputeType  = "eks.amazonaws.com/compute-type"
	LabelGKEMetadata  = "iam.gke.io/gke-metadata-server-enabled"
)

// Audiences.
const (
	AudienceSTS         = "sts.amazonaws.com"
	AudiencePodIdentity = "pods.eks.amazonaws.com"
	AudienceAzure       = "api://AzureADTokenExchange"
)

// MaxValue bounds a retained identity value (S5 §6): past it a value is
// oversized and its mechanism unresolved.
const MaxValue = 512

// Value is one identity value: the exact bytes for comparison, kept
// unexported so they never serialise, and what may be shown.
type Value struct {
	raw   string
	Shown string `json:"shown"`
	// Unexpected: the value does not have its documented shape, so it is
	// not shown (S5 §7); it still compares by its exact bytes.
	Unexpected bool `json:"unexpected,omitempty"`
}

// Equal compares exact bytes.
func (v Value) Equal(o Value) bool { return v.raw == o.raw }

// Empty reports an empty value.
func (v Value) Empty() bool { return v.raw == "" }

const notShown = "unexpected value (not shown)"

var (
	arnRe    = regexp.MustCompile(`^arn:aws[a-z0-9-]*:iam::\d{12}:role/[\w+=,.@/-]{1,512}$`)
	guidRe   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	domainRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)+[A-Za-z]{2,63}$`)
	regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d{1,2}$`)
	pathRe   = regexp.MustCompile(`^(/|[A-Za-z]:\\)[A-Za-z0-9._/\\-]{0,500}$`)
	tokenRe  = regexp.MustCompile(`^[A-Za-z0-9:/._-]{1,256}$`)
	intRe    = regexp.MustCompile(`^\d{1,9}$`)
	emailRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}@[a-z0-9][a-z0-9.-]{0,252}\.[a-z]{2,63}$`)
)

// shape is a value's documented shape.
type shape int

const (
	shapeARN shape = iota
	shapeGUID
	shapeTenant // a GUID or a domain
	shapeRegion
	shapePath
	shapeURL
	shapeToken // an audience or another short token
	shapeInt
	shapeBool
	shapeEmail
	shapeSTS
)

var envShape = map[string]shape{EnvRoleARN: shapeARN, EnvWebTokenFile: shapePath, EnvCredsURI: shapeURL, EnvCredsToken: shapePath,
	EnvRegion: shapeRegion, EnvDefaultRegion: shapeRegion, EnvSTSRegional: shapeSTS, EnvAzureClient: shapeGUID,
	EnvAzureTenant: shapeTenant, EnvAzureToken: shapePath, EnvAzureAuthHost: shapeURL}

// documentedURLs are shown as they are; any other URL shows its origin
// only, its path or query withheld.
var documentedURLs = map[string]bool{"http://169.254.170.23/v1/credentials": true, "http://[fd00:ec2::23]/v1/credentials": true}

// valueOf validates s against its shape (S5 §7): a value is shown only in
// its documented shape; a URL through the outbound URL policy.
func valueOf(s string, sh shape) Value {
	v := Value{raw: s}
	ok := false
	switch sh {
	case shapeARN:
		ok = arnRe.MatchString(s)
	case shapeGUID:
		ok = guidRe.MatchString(s)
	case shapeTenant:
		ok = guidRe.MatchString(s) || domainRe.MatchString(s)
	case shapeRegion:
		ok = regionRe.MatchString(s)
	case shapePath:
		ok = pathRe.MatchString(s) && !strings.Contains(s, "..")
	case shapeToken:
		ok = tokenRe.MatchString(s)
	case shapeInt:
		ok = intRe.MatchString(s)
	case shapeBool:
		ok = s == "true" || s == "false"
	case shapeEmail:
		ok = emailRe.MatchString(s)
	case shapeSTS:
		ok = s == "regional" || s == "legacy"
	case shapeURL:
		v.Shown, v.Unexpected = shownURL(s)
		return v
	}
	if ok {
		v.Shown = s
	} else {
		v.Shown, v.Unexpected = notShown, true
	}
	return v
}

// shownURL applies the outbound URL policy: a documented URL as it is;
// otherwise its origin (user info removed), with any path or query said as
// withheld; a value that is not a plain http(s) URL is not shown.
func shownURL(s string) (string, bool) {
	if documentedURLs[s] {
		return s, false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return notShown, true
	}
	origin := urlscrub.OriginOf(s)
	if origin == "" || origin == urlscrub.OriginWithheld {
		return notShown, true
	}
	switch {
	case (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "":
		return origin + " (custom path, not shown)", true
	case u.User != nil:
		return origin + " (user info removed)", true
	}
	return origin, false
}
