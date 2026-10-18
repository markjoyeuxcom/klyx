# Coverage C2 and S5: management is not health; workload identity

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), contract C2 and slice S5. C2 lands first, as the contract S5 uses. Revision 1 answers Codex's design review of 9b6f020 (§10).

The roadmap's S5:
- **Provider adapters, non-exhaustive:** EKS IRSA; EKS Pod Identity (in-cluster evidence only; EKS Auto Mode has it without a user-installed agent DaemonSet, C2); Azure Workload Identity (its label matters only where Azure is opted in); GKE (the IAM service account annotation, while direct federation needs none).
- **The chain:** service account, then pods, then the ESO stores that authenticate through it. Klyx compares each pod's *current* injected configuration with the service account's *current* declared identity and never infers when an annotation changed. The authenticating subject is resolved per provider and version: the ESO controller's own identity, or an explicit `serviceAccountRef`. KEDA authentication joins are S9's.
- **Never read** token contents, general Secret values or env values. Cloud trust and effective permissions stay explicitly unverified.

## 1. The API, read from the source

Read at: amazon-eks-pod-identity-webhook v0.6.17, eks-pod-identity-agent v0.2.5, azure-workload-identity v1.6.3 (with v0.15.0 to v1.6.1 for changes), External Secrets Operator v2.13.0 (chart 2.13.0), k8s.io/api v0.37.1, and the EKS, AKS, GKE and Kubernetes documentation pinned by commit or fetch date. Notes: the S5 research file (scratchpad `s5-research.md`, with every file:line citation) and Prax reference 99a45e6b ("Workload identity evidence facts for S5").

**EKS IRSA** (the AWS webhook):
- **The ServiceAccount declares:** `eks.amazonaws.com/role-arn`, `audience` (default `sts.amazonaws.com`), `sts-regional-endpoints`, `token-expiration`. A self-hosted webhook can also read a `pod-identity-webhook` ConfigMap; there the annotation wins.
- **The pod receives**, per container: `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE`, sometimes `AWS_STS_REGIONAL_ENDPOINTS` and the region vars; a projected `serviceAccountToken` volume (`aws-iam-token` by default; the name is a flag) mounted at `/var/run/secrets/eks.amazonaws.com/serviceaccount`.
- **A container that already defines either of the pair** gets neither of it. `eks.amazonaws.com/skip-containers` (comma-separated) excludes containers; the self-hosted manifest's object selector skips pods labelled `eks.amazonaws.com/skip-pod-identity-webhook` (the EKS configuration's selector is unverified).

**EKS Pod Identity:**
- **No in-cluster record:** "There isn't any data or metadata about the associations inside the cluster in any Kubernetes objects" (EKS user guide). The association lives in the AWS API.
- **The pod receives:** `AWS_CONTAINER_CREDENTIALS_FULL_URI` (`http://169.254.170.23/v1/credentials` on IPv4; the IPv6 form EKS injects is unverified), `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE`, and a projected token volume (`eks-pod-identity-token`, audience `pods.eks.amazonaws.com`). From webhook v0.6.15 also the pod label `eks.amazonaws.com/pod-identity: enabled`; which build EKS runs is unverified, so the label is evidence when present, never required.
- **Precedence:** the webhook injects Pod Identity instead of IRSA when both apply. The SDK prefers whichever credentials come earlier in its chain, so a hand-set or older IRSA env keeps winning.
- **The agent:** DaemonSet `eks-pod-identity-agent` in `kube-system` (the add-on), host network, link-local `169.254.170.23` and `[fd00:ec2::23]`. Its chart's affinity keeps it off nodes labelled `eks.amazonaws.com/compute-type` in `fargate`, `hybrid` or `auto`.
- **EKS Auto Mode:** "You do not need to install the EKS Pod Identity Agent on EKS Auto Mode Clusters. This capability is built into EKS Auto Mode." An Auto Mode node carries `eks.amazonaws.com/compute-type: auto` ("Identifies EKS Auto Mode managed nodes"). How the agent runs there is not documented.

**Azure Workload Identity:**
- **The ServiceAccount declares:** `azure.workload.identity/client-id`, `tenant-id` (default: the webhook's own), `service-account-token-expiration` (an out-of-range value makes the webhook reject the pod).
- **Opt-in is per pod:** the pod label `azure.workload.identity/use: "true"` (exact value), required since v1.0.0, enforced by the webhook configuration's object selector. The annotated ServiceAccount alone does nothing. The webhook's `failurePolicy` is `Fail`.
- **The pod receives:** `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_FEDERATED_TOKEN_FILE`, `AZURE_AUTHORITY_HOST`; a projected token volume with audience `api://AzureADTokenExchange`. Its name and path changed in v1.6.0 and v1.6.1 and are now hashed (`azure-workload-identity-reserved-<hash>`; `azure-identity-token` before): matched by the prefix or the old name, and the audience, never a fixed path.
- **Existing env wins** per variable. `azure.workload.identity/skip-containers` is semicolon-separated.
- **The webhook:** self-hosted Deployment `azure-wi-webhook-controller-manager` in `azure-workload-identity-system`; configuration `azure-wi-webhook-mutating-webhook-configuration`. The AKS add-on's Deployment is unverified (the docs show its VPA in `kube-system`).

**GKE Workload Identity Federation:**
- **Direct principal access** needs no annotation; the IAM allow policy names the Kubernetes ServiceAccount. The **impersonation alternative** annotates it with `iam.gke.io/gcp-service-account`.
- **Nothing is written into the pod** (an inference from the documented interception by the GKE metadata server, `gke-metadata-server` pods in `kube-system`); a `hostNetwork` pod does not use it. Node pools with the metadata server are selected with `iam.gke.io/gke-metadata-server-enabled: "true"` (that GKE sets the label is implied by the documented selector).

**Every webhook mutates at pod CREATE only.** EKS and AKS both document that pods must be recreated after an annotation change.

**External Secrets Operator** (served `external-secrets.io/v1`; `v1beta1` unserved by default, its field paths for these are the same):
- **AWS** (`spec.provider.aws.auth`): `jwt.serviceAccountRef` (ESO mints that ServiceAccount's token itself; it requires the `eks.amazonaws.com/role-arn` annotation and cannot use Pod Identity); `secretRef` (static keys); none (the controller's own SDK default chain: its own IRSA, Pod Identity or node role).
- **Azure Key Vault:** `authType` `ServicePrincipal`, `ManagedIdentity` or `WorkloadIdentity`. `WorkloadIdentity` with `serviceAccountRef` takes the client ID from `authSecretRef.clientId` or the ServiceAccount's annotation (both is an error); without it, ESO reads `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE` from the controller's own environment, failing when any is missing.
- **GCP Secret Manager** (`spec.provider.gcpsm.auth`): `secretRef`, then `workloadIdentity.serviceAccountRef`, then `workloadIdentityFederation` (v1 only; one of `credConfig`, `serviceAccountRef`, `awsSecurityCredentials`), else the controller's ADC.
- **`serviceAccountRef.namespace`:** on a SecretStore, empty or its own namespace (anything else fails validation); on a ClusterSecretStore, optional, and omitted means each ExternalSecret's own namespace.
- **The controller:** chart Deployment and ServiceAccount `external-secrets` in the release namespace (Klyx's capability already probes `external-secrets/external-secrets`).

## 2. C2: management is not health

**The contract,** a type in `internal/capability` (`Management`) used by every slice that reads platform components:
- **Two facts, kept separate:**
  - **manager:** `self-hosted`, `platform` or `unknown`;
  - **observed:** `observed` (Klyx can read the component's health) or `not-observed` (it cannot: a webhook served by URL, a capability built into the node).
- **Evidence:** each fact carries the evidence it rests on, said in the UI. Platform management is claimed only on evidence that distinguishes it; anything less is `unknown`.
- **Management never suppresses a fault:** an observed component that is not ready reports it, whoever manages it; a forbidden read stays unreadable, never "managed".
- **Where health cannot be observed**, the reading is "managed by the platform; health not directly observed" (or "manager unverified; health not directly observed"): neither healthy nor failed.

**The first evidence table** (S5's platforms):

| Component | Evidence read | manager | observed |
|---|---|---|---|
| Pod Identity on a node labelled `eks.amazonaws.com/compute-type: auto` | the node label, and the EKS user guide's "built into EKS Auto Mode" | platform (EKS Auto Mode) | not-observed |
| The `eks-pod-identity-agent` DaemonSet in `kube-system` | the DaemonSet and its pods on the workload's node | unknown (the add-on's labels are unverified) | observed: its pod on that node, ready or not |
| A mutating webhook served by URL (`clientConfig.url`) | the configuration | unknown (a URL proves only that it is not an in-cluster Service) | not-observed |
| A mutating webhook served by an in-cluster Service | the configuration | unknown | observed, by the Admission lens's backend verdict (S3a); Klyx links to it, never repeats it |
| The GKE metadata server | a `gke-metadata-server-*` pod in `kube-system` on the workload's node, on a node whose providerID is `gce://` | platform (GKE) | observed: that pod, ready or not |

**Not in C2 here:**
- **A controller task's "managed" state** (the task-level reading for a managed controller) arrives with the first task that needs one, S10's node provisioning (EKS Auto Mode's Karpenter, AKS NAP). S5 adds no controller task: its reads are on demand.
- **Karpenter on Auto Mode** stays as it is (controller unverified) until S10, said there.

## 3. The identity reading

**Acquisition:** on demand only (§6), never watched, never a verdict, never in Needs you or notifications. It is evidence, qualified.

**3.1 What a ServiceAccount declares,** per adapter, from its current annotations:
- **EKS IRSA:** the role, the audience, regional STS, the expiration.
- **Azure:** the client ID, the tenant ID, the expiration (out of range is said: the webhook rejects such pods).
- **GKE:** the linked Google service account (impersonation), or none: "direct principal access may still be granted in IAM, which Klyx cannot see".
- **EKS Pod Identity** declares nothing on the ServiceAccount; it is said so every time ("associations live in AWS").
- Its `automountServiceAccountToken` is shown; it does not govern the webhooks' own token volumes (an inference from their explicit projected volumes, said as such).

**3.2 What a pod was given** (revision 1), per container, from the pod's current spec:
- **A mechanism is recognised only through the container's own token-file chain:** its file variable's literal value resolves, through one of that container's `volumeMounts` (`mountPath`, and `subPath` where set), to a `serviceAccountToken` entry of a projected volume of the pod, with that entry's `path` and the mechanism's audience. The three chains:
  - **IRSA:** `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE`; any audience, compared with the declared one (default `sts.amazonaws.com`);
  - **Pod Identity:** `AWS_CONTAINER_CREDENTIALS_FULL_URI` and `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE`; audience `pods.eks.amazonaws.com`; the pod label is evidence when present;
  - **Azure:** `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` and `AZURE_FEDERATED_TOKEN_FILE`; audience `api://AzureADTokenExchange`; the volume found by its chain, whatever its name (old name or the hashed one).
- **Partial:** some of a mechanism's variables without the rest, or a file variable whose path resolves to no mounted token entry, to an unmounted volume, to another audience, or to another file. Each says which link is missing.
- **Unresolved, never partial or matched:** a named variable set by `valueFrom`, one holding `$(VAR)` expansion, an oversized value (§6), or a mount Klyx cannot resolve. `envFrom` and the image's own environment can supply any of these variables invisibly; when a mechanism's variable is absent from `env`, the reading says "not in the pod spec's env; envFrom sources and the image's defaults are not read".
- **Both AWS mechanisms in one container:** both are reported; which one the SDK uses is never inferred.
- **Exclusions,** each from its own evidence: a container named in `skip-containers` (comma on AWS, semicolon on Azure); the AWS opt-out label; an Azure pod without the exact `"true"` label; a `hostNetwork` pod on GKE.
- **Origin is never claimed.** The webhook, the pod template and another tool can all write the same spec, so Klyx says what the pod has, not who wrote it.

**3.3 The comparison** (revision 1), current against current, per container and adapter. It names exactly what it compared:

| Pod has (a complete chain) | ServiceAccount declares now | Reading |
|---|---|---|
| IRSA, role X, audience A | role X, audience A | "Role and audience match the ServiceAccount." |
| IRSA, role X | role Y (or another audience) | "Differs: the pod carries X; the ServiceAccount now declares Y. Pods take identity when created; one created before the change keeps X. A value set in the pod template also wins over the webhook." |
| IRSA, role X | no role | "Configured with X; the ServiceAccount declares no role now (removed later, set in the template, or a self-hosted webhook's ConfigMap)." |
| Pod Identity | anything | "EKS Pod Identity configured. Its association and role live in AWS: not visible." With a declared IRSA role: "The webhook prefers Pod Identity over IRSA when both apply." |
| none | role Y | Each evidenced exclusion first; otherwise "Not configured in the pod spec: it was created at T. A pod created before the annotation keeps none; when the annotation was set is not visible." Plus the envFrom/image limit. |
| Azure, client X, tenant T | client X and tenant T | "Client ID and tenant match." |
| Azure, client X, tenant T | client X, no tenant declared | "Client ID matches; the tenant is the webhook's default, which is not visible." |
| Azure, client X or tenant T | another client or tenant | "Differs" (as IRSA), naming which. |
| none, Azure declared | client Y | Without the pod label: "Not opted in: the ServiceAccount annotation alone does nothing; the pod needs `azure.workload.identity/use: "true"`." With it: as IRSA's "not configured". |
| partial or unresolved | anything | The missing link or the unresolved value, and no comparison. |
| (GKE) | linked GSA G, or none | "Linked to G for impersonation; the IAM binding is not visible", or "No link declared; direct principal access may still be granted in IAM, not visible." Plus the node (3.4). |

The comparison runs on the exact retained values (§6), never on display clips.

**Never inferred:** when an annotation changed (managedFields times cover other changes), whether a role trusts the cluster, whether an association exists, whether credentials work, which credential the SDK picks. The limits block says each, every time.

**3.4 The node side** (C2), for a pod using an AWS or GKE mechanism:
- **Pod Identity:** the pod's node. Auto Mode (its label): "built into EKS Auto Mode; health not directly observed". Otherwise the agent DaemonSet's pod on that node: ready, not ready (said as evidence the workload will feel, not a verdict), or none ("no Pod Identity agent pod on this node"). Fargate and Windows nodes, from their labels: "EKS Pod Identity is not supported on this node".
- **GKE:** the node's metadata-server label. On a Linux node, its `gke-metadata-server` pod, ready or not. On a Windows node the metadata server is a native Windows service, so no pod is expected: "runs as a Windows service; not directly observed".
- **The webhook** (`pod-identity-webhook`, `azure-wi-webhook-mutating-webhook-configuration`): present or not, how it is served, and a link to its Admission verdict when Service-backed (§2's table). Azure's consequence is worded from the configuration Klyx read (S3a's projection): its failure policy and whether object or namespace selectors narrow it. For example: "failurePolicy Fail: while its backend is unavailable, pod creations this webhook matches are refused".

## 4. The ESO chain: who authenticates (revision 1)

**4.1 The decision table, from ESO's source per provider and mode.** Each store reads as a base credential and then any requested principal on top of it. A path Klyx cannot resolve says so, and never guesses.

| Provider, mode | Base credential | Requested principal | Unresolved or refused |
|---|---|---|---|
| AWS, `auth.jwt.serviceAccountRef` | that ServiceAccount (namespace as §1); ESO mints its token, so Pod Identity cannot apply; its `eks.amazonaws.com/role-arn` | `role`, then each of `additionalRoles` (AssumeRole on top, in order) | the ServiceAccount lacks `role-arn`: ESO refuses ("an IAM role must be associated") |
| AWS, `auth.secretRef` | static keys from a Secret (never read) | `role`, `additionalRoles` | values unread |
| AWS, no auth | the controller's credential chain (4.2), a configured source, not a proven principal | `role`, `additionalRoles` | the chain's effective credential is not inferred |
| Azure, `WorkloadIdentity`, `useAzureSDK: true` | the ServiceAccount (`serviceAccountRef`): client ID from its annotation; tenant from the store's `tenantId`, then the ServiceAccount's | none | without `serviceAccountRef`: the controller's environment (4.2) |
| Azure, `WorkloadIdentity`, legacy SDK | the ServiceAccount: client ID from `authSecretRef.clientId` (a Secret, unread) or its annotation; tenant from `authSecretRef.tenantId`, then the store, then the ServiceAccount, then the controller's env | none | a Secret-sourced ID is unresolved; the legacy duplicate-client-ID refusal depends on the Secret's value, so it is never claimed |
| Azure, `ManagedIdentity`, `ServicePrincipal` | named, not followed | none | |
| GCP, `secretRef` | static (never read) | none | |
| GCP, `workloadIdentity` | the ServiceAccount (`serviceAccountRef`) | its linked GSA (`iam.gke.io/gcp-service-account`) if any, else the federated principal | |
| GCP, `workloadIdentityFederation` (v1 only) | `serviceAccountRef`, `credConfig` (a ConfigMap, unread) or `awsSecurityCredentials` | `gcpServiceAccountEmail` when set (it overrides the ServiceAccount's linked GSA), else that link | credConfig and AWS credentials unresolved |
| GCP, no auth | the controller's ADC (4.2) | none | not inferred |
| any other provider | | | "not read by Klyx's identity chain" |

**Validation ESO enforces, said when violated:** a namespaced store's `serviceAccountRef.namespace` must be empty or its own. A ClusterSecretStore with no namespace resolves per ExternalSecret ("each ExternalSecret's own namespace"), never one guessed ServiceAccount.

**4.2 Which controller handles a store, and its identity** (revision 1). ESO processes a store when the store's `spec.controller` is empty or equals the controller's `--controller-class` (default `default`; ESO `ShouldProcessStore`, `--controller-class` in cmd/controller/root.go). Chart names and namespaces are configurable. So:
- **Candidates:** Deployments labelled `app.kubernetes.io/name=external-secrets` (the chart's controller selector label; the webhook and cert controller carry `-webhook` and `-cert-controller`) in any namespace, plus the capability's conventional `external-secrets/external-secrets`. At most 3 candidates are read, and more are said.
- **Applicable:** a candidate whose class (its container's `--controller-class` argument, else `default`) the store accepts. An empty `spec.controller` accepts every candidate. A class set by environment or a config file is unresolved.
- **Exactly one applicable candidate:** its running pods, owned by its current ReplicaSets (fenced by owner UID), grouped by each pod's own `serviceAccountName` (a rolling update can run two). Each group's pods are read as §3.2 does.
- **None, several, or unresolved:** "the controller handling this store is not resolved (N candidates)". The subject is never named, and the inverse join (4.3) applies the same rule.
- **The controller's environment** (Azure without `serviceAccountRef`): a variable absent from those pods' `env` reads "not in the pod spec; envFrom and the image's environment are not read", never "ESO will fail".

**4.3 From a ServiceAccount:** the stores whose `serviceAccountRef` names it, by the table's rules (a SecretStore in its namespace; a ClusterSecretStore naming its namespace, or naming none, said as "for ExternalSecrets in this namespace"). If it is the ServiceAccount of a resolved controller's pods, also every store that controller handles with no auth: "authenticate through this ServiceAccount's credential chain".

**Version:** stores come from the ESO lens's kinds (`external-secrets.io/v1`, or `v1beta1` where only that is served; the paths are the same, and v1-only fields are absent there).

## 5. What Klyx shows

No new lens. Three inspector blocks, each read when its inspector opens and again on request:
- **ServiceAccount** (the generic inspector): "Identity":
  - its declarations per adapter;
  - its pods (§6's page, each with its reading);
  - the node and webhook side;
  - the ESO stores that use it;
  - the limits.
- **Pod** (the Pods lens inspector): "Identity": its own containers' readings against its ServiceAccount's current declarations, and its node's side.
- **ESO store** (the External Secrets lens inspector): "Authenticates as": the base credential, any requested principal, the controller resolution, and a link to the ServiceAccount's identity.

**The limits, always said:**
- cloud trust, associations and permissions are not visible;
- identity is given at pod creation, and when an annotation changed is not visible;
- a value in the pod spec wins over the webhook, and Klyx cannot tell which wrote it;
- envFrom sources and the image's environment are not read;
- which credential the SDK picks is not inferred;
- only EKS, Azure and GKE are read.

## 6. The source contract (C1), the reads and their bounds (revision 1)

| Read | How | Identity | Gaps |
|---|---|---|---|
| The ServiceAccount | GET, fenced by the UID the inspector holds (another UID: "replaced") | its UID | forbidden or failed: "unreadable", never "no identity" |
| Its pods | LIST in its namespace, `fieldSelector=spec.serviceAccountName=<name>`, `limit=50` | each pod's UID | forbidden: unreadable. With `continue` set: "more pods not read". The count is unknown, because the API omits `remainingItemCount` for selected lists. |
| One pod (the Pods lens) | GET, fenced by UID; then its ServiceAccount (GET) | UIDs | as above |
| Nodes | the Observer's nodes model (labels, providerID), no read | node name | not live: "node evidence unverified" |
| The agent and the metadata server | the agent DaemonSet (GET), only when an AWS mechanism is in play; its pods and the metadata-server pods from the Observer's pods model | names | not read: unverified |
| The webhook configurations | the Observer's admission kinds (S3a's projection) | their names | not read: unverified |
| The ESO stores | the Observer's ESO kinds, when ESO is installed | store UIDs | not complete: "stores unverified" |
| The ESO controllers | candidate Deployments (LIST by label, limit 3; GET the conventional one); for the one applicable controller, its pods (LIST by its selector, limit 10) and their ReplicaSet owners (from the Observer's ReplicaSets model) | UIDs | a gap leaves the subject unresolved |

**Order:** the API returns a page in its own key order (by name), not by age. The page is the first 50 by name; Klyx sorts within it by age and says so ("50 of more, by name; newest first within them").

**Bounds on content**, each with its cut said:
- containers and init containers read per pod: 16;
- identity variables per container: the named set only (§3.2);
- a retained identity value: 512 bytes. Past that the value is "oversized", the mechanism is unresolved, and nothing is compared;
- projected volume sources per pod: 16;
- ESO stores listed per ServiceAccount: 20;
- the DTO: 256 KB, past which pods are dropped from the end with the count said.

The comparison uses exact retained values; display clipping comes after.

**Clock:** none. A read says when it was taken. A newer read replaces it, fenced by cluster, UID and request (the render-time fence of S3a's param check).

**Budget:** no new watches and no retained model. Per inspector read, at most:
- 1 ServiceAccount GET;
- 1 pods LIST of 50;
- 1 DaemonSet GET;
- 1 Deployment LIST of 3 and 1 GET;
- 1 pods LIST of 10.

## 7. Sensitive data (revision 1)

**A narrow, recorded exception to the roadmap's "never read env values":** the named identity variables of §3.2 (with the region and STS ones) are read with their values. Every other variable is dropped at parse time, name and value. `envFrom` sources, `valueFrom` targets, Secret and ConfigMap contents, mounted files and token contents are never read; Klyx never calls TokenRequest.

**A variable's name never vouches for its value.** A hand-set value is kept by the webhooks, so an allowlisted variable can carry credentials. Before a value reaches the DTO:
- **URL-valued** (`AWS_CONTAINER_CREDENTIALS_FULL_URI`, `AZURE_AUTHORITY_HOST`): the outbound URL policy (`urlscrub.Field`). User info is removed, and an undelimitable value is withheld. A query string or a path beyond the documented shape reads "custom path (not shown)".
- **Shaped identifiers:** `AWS_ROLE_ARN` must match the ARN form (`arn:aws[a-z-]*:iam::<12 digits>:role/...`); `AZURE_CLIENT_ID` and `AZURE_TENANT_ID` a GUID (or, for the tenant, a domain name); a region a region token; a file path an absolute path. Anything else reads "unexpected value (not shown)", and the comparison uses its exact bytes only for equality, never displaying them.
- **The same policy** applies to the ServiceAccount's annotations and the ESO store fields shown.

**AI and chat:** nothing of S5 is sent; there is no Explain over these reads. The pod and ServiceAccount Explain targets keep their existing projections, which carry no env.

## 8. Tests, lab, parity, smoke

- **`internal/identity` (pure), unit:**
  - every row of §3.3 per adapter;
  - the env-pair rule, a partial mechanism, `valueFrom`, both separators, the exact Azure label, the old and hashed Azure volumes, the Windows token path;
  - Pod Identity precedence and the optional v0.6.15 label;
  - GKE `hostNetwork`;
  - the C2 table's rows, including an observed agent not ready on a managed cluster (never suppressed);
  - every ESO subject case of §4 (each provider, store kind and namespace rule, ESO's refusals);
  - revision 1:
    - the chains: a wrong file, an unmounted volume, another audience, a `subPath`, `valueFrom`, `$(VAR)` expansion, an oversized value, envFrom-only, an Azure tenant mismatch, and both AWS mechanisms in one container;
    - ESO's overrides: `useAzureSDK` true and legacy, a Secret-sourced client ID left unresolved, `gcpServiceAccountEmail` over the linked GSA, and AWS `role` and `additionalRoles` on each base;
    - the controller: one, none, several and unresolved candidates, `spec.controller` classes, and a rolling update with two ServiceAccounts;
    - unexpected values: an ARN, GUID, URL with user info and custom path, in allowed variables, read as not shown.
- **`fleet`:** the reads with fake clients: the UID fence, the field selector, the cap, a continuation without `remainingItemCount` (more, count unknown), the content bounds, and forbidden as unreadable.
- **appbridge:** the DTO and its 256 KB bound, and a literal-needle test: no non-identity env value, Secret value or token content crosses, and no credential planted in an allowed variable (URL user info, a query) crosses either.
- **Frontend:** the three blocks, their limits, and a fenced reply.
- **The lab (`lab-s5`, kind-klyx), fixtures per C6** (the webhooks are not lab components there; their output shapes are cited above):
  - ServiceAccounts with IRSA, Azure and GKE annotations;
  - pods in each §3.3 row, their env and volumes written in each webhook's documented shape (current, a stale role, created before the annotation, a skipped container, partial, an Azure pod without the label, Pod Identity with and without the pod label);
  - a stand-in `eks-pod-identity-agent` DaemonSet and a stand-in `pod-identity-webhook` configuration served by URL;
  - ESO stores of each auth shape on the ESO CRDs already in the lab, with a stand-in controller Deployment (the chart's label, a `--controller-class`) whose pods carry identity env.

  Not in the lab: an Auto Mode node, a Fargate node and GKE nodes. Relabelling kind's single node would change every other lab reading, so they are unit-tested.
- **Parity:** S5 adds no part, so the gate is unchanged.
- **Native smoke:** a lab ServiceAccount's Identity block, a stale-role pod, and an ESO store's subject.

## 9. Not in S5

- **AWS association reads** (an AWS API) and any cloud call: out of the programme.
- **KEDA authentication** (TriggerAuthentication): S9.
- **The controller-issue "managed" state and Karpenter on Auto Mode:** S10.
- **Other identity systems** (Vault Kubernetes auth, aad-pod-identity beyond naming `ManagedIdentity`, SPIFFE): named as not read.
- **Verbs:** none. Recreating pods to pick up an identity is the user's, through the existing restart verb.

## 10. Revision 1: Codex's design review of 9b6f020

| # | Finding | Where answered |
|---|---|---|
| 1 | The subject resolver did not follow ESO's provider branches: `useAzureSDK`, `gcpServiceAccountEmail`, AWS `role`/`additionalRoles` | §4.1: a per-provider, per-mode decision table of base credential and requested principal, from ESO's source. Secret-sourced IDs and the value-dependent legacy refusal stay unresolved. §8: the overrides' tests |
| 2 | The controller binding was assumed from the conventional Deployment | §4.2: candidates by the chart's label and the conventional name; applicability by `spec.controller` against each candidate's `--controller-class`; one applicable candidate or the subject unresolved; pods fenced by ReplicaSet owner UID and grouped by their own ServiceAccount; the inverse join under the same rule |
| 3 | An env variable's absence from the pod spec does not prove the process lacks it | §3.2 and §4.2: "not in the pod spec's env; envFrom and the image's environment are not read", never "ESO will fail"; the SDK default chain is a configured source, not a proven principal |
| 4 | Mechanism recognition needed each container's token-file chain, and the comparison a defined scope | §3.2: env to mount (with `subPath`) to projected token entry and audience, per container; partial vs unresolved; Azure's complete shape includes the tenant. §3.3 names what it compared (role and audience; client ID and tenant) |
| 5 | The capped read over-promised "+N not read" and "newest 50", and content was unbounded | §6: "more, count unknown" (no `remainingItemCount` for selected lists); the first 50 by name, sorted by age within; bounds on containers, values, volume sources, stores and the DTO; exact comparison before display clipping |
| 6 | An allowlisted variable's value can still carry credentials | §7: the recorded, narrow exception to "never read env values"; the URL policy for URL-valued variables; shaped-identifier validation, with "unexpected value (not shown)"; needles in allowed variables (§8) |
| (guidance) | GKE on Windows; Azure's admission consequence from its actual configuration | §3.4 |
