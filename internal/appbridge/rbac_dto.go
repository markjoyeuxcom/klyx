package appbridge

// AccessQueryDTO is the access-check request from the webview, in
// ResourceAttributes vocabulary. SAName empty means "check as the kubeconfig
// identity"; set, the check impersonates system:serviceaccount:SANamespace:SAName.
type AccessQueryDTO struct {
	Verb        string `json:"verb"`
	Group       string `json:"group"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	SANamespace string `json:"saNamespace"`
	SAName      string `json:"saName"`
}

// AccessVerdictDTO mirrors rbac.Verdict plus the resolved subject (so the UI
// states WHO was checked, never implies) and a fold-in error.
type AccessVerdictDTO struct {
	Allowed         bool   `json:"allowed"`
	Denied          bool   `json:"denied"`
	Reason          string `json:"reason"`
	EvaluationError string `json:"evaluationError"`
	Subject         string `json:"subject"`
	Error           string `json:"error"`
}

// AccessGrantDTO mirrors rbac.Grant.
type AccessGrantDTO struct {
	SubjectKind      string `json:"subjectKind"`
	SubjectName      string `json:"subjectName"`
	SubjectNamespace string `json:"subjectNamespace"`
	BindingKind      string `json:"bindingKind"`
	BindingNamespace string `json:"bindingNamespace"`
	BindingName      string `json:"bindingName"`
	RoleKind         string `json:"roleKind"`
	RoleName         string `json:"roleName"`
}

// AccessWhoCanDTO mirrors rbac.Enumeration plus a fold-in error.
type AccessWhoCanDTO struct {
	Grants          []AccessGrantDTO `json:"grants"`
	ClusterWideOnly bool             `json:"clusterWideOnly"`
	Error           string           `json:"error"`
}
