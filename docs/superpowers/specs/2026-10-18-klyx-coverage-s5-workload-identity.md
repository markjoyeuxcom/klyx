# Coverage C2 and S5: management is not health; workload identity

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), contract C2 and slice S5. C2 lands first, as the contract S5 uses. Revision 1 answers Codex's design review of 9b6f020 (§10), revision 2 its re-review (§11), revision 3 the next (§12).

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
| Azure, client X, tenant T | client X, no tenant declared | "Client ID matches. The pod carries tenant T; the ServiceAccount declares no tenant now. Where T came from (the webhook's default, a hand-set value, an earlier annotation) is not visible." (revision 2) |
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

**4.1 The decision table, from ESO's source per provider and mode.** Each store reads as a base credential and then any requested principal on top of it. A path Klyx cannot resolve says so, and never guesses. Revision 2: AWS assumes each of `additionalRoles` in its listed order, then `role` (`aws/auth/auth.go` 110-130), so for base A, `additionalRoles` [B, C] and `role` D the chain is A, B, C, D, and the final requested principal is D, else the last additional role.

| Provider, mode | Base credential | Requested principal | Unresolved or refused |
|---|---|---|---|
| AWS, `auth.jwt.serviceAccountRef` | that ServiceAccount (namespace as §1); ESO mints its token, so Pod Identity cannot apply; its `eks.amazonaws.com/role-arn` | each of `additionalRoles` in order, then `role` (AssumeRole on top; the last one assumed is the requested principal) | the ServiceAccount lacks `role-arn`: ESO refuses ("an IAM role must be associated") |
| AWS, `auth.secretRef` | static keys from a Secret (never read) | each of `additionalRoles` in order, then `role` | values unread |
| AWS, no auth | the controller's credential chain (4.2), a configured source, not a proven principal | each of `additionalRoles` in order, then `role` | the chain's effective credential is not inferred |
| Azure, `WorkloadIdentity`, `useAzureSDK: true` | the ServiceAccount (`serviceAccountRef`): client ID from its annotation only; tenant from the store's `tenantId` whenever the field is set (an empty string included, used as is), else the ServiceAccount's annotation (`keyvault_new_sdk.go` 548-566) | none | no client annotation, or neither tenant source: ESO refuses (deterministic). Without `serviceAccountRef`: the controller's environment (4.2) |
| Azure, `WorkloadIdentity`, legacy SDK, with `serviceAccountRef` | the ServiceAccount, by the guards below (`keyvault.go` 1111-1195) | none | the guards below |
| Azure, `WorkloadIdentity`, legacy SDK, no `serviceAccountRef` | the controller's own environment (`AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_FEDERATED_TOKEN_FILE`), read as 4.2 says | none | an absent variable reads "not in the pod spec" (4.2), never a refusal |
| Azure, `ManagedIdentity`, `ServicePrincipal` | named, not followed | none | |
| GCP, `secretRef` | static (never read) | none | |
| GCP, `workloadIdentity` | the ServiceAccount (`serviceAccountRef`) | its linked GSA (`iam.gke.io/gcp-service-account`) if any, else the federated principal | |
| GCP, `workloadIdentityFederation` (v1 only) | `serviceAccountRef`, `credConfig` (a ConfigMap, unread) or `awsSecurityCredentials` | `gcpServiceAccountEmail` when set (it overrides the ServiceAccount's linked GSA), else that link | credConfig and AWS credentials unresolved |
| GCP, no auth | the controller's ADC (4.2) | none | not inferred |
| any other provider | | | "not read by Klyx's identity chain" |

**The legacy Azure guards** (revision 3), in ESO's order, with `serviceAccountRef` set (`keyvault.go` 1111-1195):
- **Client ID:**
  - `authSecretRef` present without a `clientId` reference (`{}`, or tenant-only): ESO refuses (`errMissingClientIDSecret`) before it looks at the annotation. Deterministic.
  - `authSecretRef.clientId` present: the client ID is that Secret's value, unread. With the client annotation also present, ESO refuses only if the value is non-empty, so this is unresolved and never claimed.
  - No `authSecretRef`: the client annotation. Absent: ESO refuses (`errMissingClient`). Deterministic.
- **Tenant,** accumulated in order:
  1. `authSecretRef.tenantId` (a Secret, unread);
  2. if still empty, the store's `tenantId` (an empty string adds nothing);
  3. the ServiceAccount's tenant annotation, refused (`errMultipleTenantID`) when the accumulated tenant is non-empty;
  4. else the controller's `AZURE_TENANT_ID`.

  Deterministic refusal: no `authSecretRef.tenantId` reference, a non-empty store `tenantId`, and the annotation present (equal values included). With a Secret-sourced tenant reference beside the annotation it is unresolved. An empty store `tenantId` falls through to the annotation and is no refusal. With no source in the store or on the ServiceAccount, the tenant comes from the controller's environment: not visible, said so.

**Validation ESO enforces, said when violated:** a namespaced store's `serviceAccountRef.namespace` must be empty or its own. A ClusterSecretStore with no namespace resolves per ExternalSecret ("each ExternalSecret's own namespace"), never one guessed ServiceAccount.

**4.2 Which controller handles a store, and its identity** (revisions 1 and 2). ESO processes a store when the store's `spec.controller` is empty or equals the class of the running controller process (`--controller-class`, default `default`; ESO `ShouldProcessStore`, cmd/controller/root.go). The class is the running process's, not the newest template's: during a class change, old and new pods can run different classes and different ServiceAccounts. So:
- **Candidates:** Deployments labelled `app.kubernetes.io/name=external-secrets` (the chart's controller selector label; the webhook and cert controller carry `-webhook` and `-cert-controller`) in any namespace, plus the conventional `external-secrets/external-secrets`. At most 3 are read, and more are said.
- **Running processes:** each candidate's pods (LIST by its selector, at most 10 per candidate), fenced to the candidate by their ReplicaSet owner's UID. Each pod's class is read from its own container's `--controller-class` argument (a pod's args are its process's, immutable); absent, `default`; an argument holding `$(VAR)` expansion, or a class set by environment or a config file, is unresolved.
- **Applicable:** the running pods whose class the store accepts (an empty `spec.controller` accepts every class). The filter comes first; identities come after.
- **The subjects:** the applicable pods grouped by each pod's own `serviceAccountName`, each group read as §3.2 does, with its pods' evidence and a link to that ServiceAccount (its declarations are not fetched for every group).
- **Unresolved, never a guessed subject:** any of the following. The inverse join (4.3) applies the same rule.
  - no applicable pod;
  - a candidate list or pod read that is incomplete (capped, continued, forbidden);
  - an unresolved class on a pod that could apply.
- **Several applicable groups** (a rolling change, or two installs): each is listed as a handler with its own identity. Klyx never picks one.
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
| The ESO controllers | candidate Deployments (LIST by label, limit 3; GET the conventional one); each candidate's pods (LIST by its selector, limit 10, at most 3 LISTs) with their ReplicaSet owners (from the Observer's ReplicaSets model) | UIDs | an incomplete read leaves the subject unresolved |

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
- 3 pods LISTs of 10 (the candidates' pods).

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
  - revision 2:
    - AWS `additionalRoles` [B, C] with `role` D on each base (the chain A, B, C, D);
    - legacy Azure tenant: the store's tenant with the annotation, equal and different (both refused), each single source, a Secret-sourced tenant beside the annotation (never claimed), and the no-`serviceAccountRef` controller-env branch;
  - revision 3: an empty `authSecretRef` and a tenant-only `authSecretRef` beside a client annotation (both refused before the annotation), and an empty-string store `tenantId` with the annotation (falls through, no refusal); the new SDK's empty store tenant used as is;
    - the new SDK's store-tenant preference;
    - a class A to B rollout with SA_A and SA_B pods running together, for a class-A store and a class-B store;
    - a capped candidate pod read, which certifies no handler;
    - Azure tenant wording with no tenant declared.
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

## 11. Revision 2: Codex's re-review of 284b22c

| # | Finding | Where answered |
|---|---|---|
| 1 | AWS's AssumeRole order was reversed | §4.1: `additionalRoles` in order, then `role`; the last assumed is the requested principal; all three AWS rows; §8 tests both fields on each base |
| 2 | Legacy Azure's tenant refusal is deterministic: a store tenant beside the ServiceAccount's annotation | §4.1: the legacy row split by `serviceAccountRef`; deterministic refusals (no Secret read) apart from the value-dependent ones; the new SDK's store-tenant preference kept separate; the no-`serviceAccountRef` controller-env branch kept |
| 3 | Applicability came from the Deployment template, identity from running pods | §4.2: each running pod's own `--controller-class`; applicability filtered per pod before grouping by ServiceAccount; an incomplete read or an unresolved class certifies no handler; several applicable groups each listed; §8 the mixed-class rollout for both stores |
| 4 (P3) | "The tenant is the webhook's default" claimed origin | §3.3: the pod carries tenant T, none is declared now, its origin is not visible |

## 12. Revision 3: Codex's re-review of ad12a3c

| # | Finding | Where answered |
|---|---|---|
| 1 | The legacy Azure guards did not match ESO's branch: an `authSecretRef` without `clientId` is refused before the annotation; an empty store `tenantId` is no duplicate | §4.1: "The legacy Azure guards", in ESO's order, deterministic refusals apart from value-dependent ones; the new SDK's empty store tenant said; §8 the three regressions |

## 13. As built (22acb85 to b4d5c96)

**C2, `capability.Management`** (22acb85): `Manager` (self-hosted, platform, unknown) and `Observability` (observed, not observed), each with its evidence. `Unobserved()` and `HealthReading()` give §2's wording. Management is positive evidence only, and nothing reads it to suppress a fault.

**`internal/identity`** (22acb85), pure:
- **Values** (`values.go`): the named variable set is the only env kept. Each value is shape-checked (ARN, GUID, domain, region, token path); a URL-valued variable goes through `urlscrub`, so a documented URL is shown and anything else shows its origin with "(custom path, not shown)" or "(user info removed)". A value that fails its shape reads "unexpected value (not shown)" but still compares by its exact bytes, which never serialise.
- **Declarations** (`declare.go`): the IRSA, Azure and GKE annotations, automount, and Azure's expiration range.
- **Pods** (`pod.go`): each container's mechanisms through env, then mount (the longest mountPath, `subPath`; `subPathExpr` unresolved), then the projected token entry and its audience. Windows paths are normalised. The bounds of §6 apply, each with its cut said.
- **The comparison** (`compare.go`): §3.3's rows per adapter, each naming what it compared.
- **The node and webhook side** (`node.go`): §3.4 through C2.
- **ESO** (`eso.go`): §4.1's decision table per provider and mode, in ESO's order. `ClassOf` reads each running pod's own `--controller-class`; `HandlersOf` applies ESO's ShouldProcessStore per pod before grouping by ServiceAccount.
- **The reports** (`report.go`): one per inspector block, with the limits.

**The reads** (30aaa60, `fleet/identity.go`) are as §6, and the budget holds:
- the ServiceAccount and the pod are UID-fenced GETs;
- the pods LIST uses the field selector with `limit=50`, and a continuation is "more, count unknown";
- nodes, the agent's and the metadata server's pods, the webhook configurations, the ESO stores and the ReplicaSets come from the Observer's models, and only a live, complete model counts as read;
- the agent DaemonSet is a GET, only when an AWS mechanism is in play;
- ESO candidates are a label LIST (limit 3) plus a GET of the conventional Deployment, then each candidate's pods (limit 10). A pod counts toward its candidate only through its ReplicaSet owner's UID, and an unread ReplicaSets model fences nothing, so it names no handler.

**appbridge** (30aaa60): `IdentityService` with `ServiceAccount`, `Pod` and `Store`. The reply's 256 KB bound drops pods from the end and says how many. A literal-needle test covers the boundary: no non-identity env value, no Secret value, and no credential planted in an allowed variable's URL user info or query crosses.

**The frontend** (0db4d9a): the three blocks of §5:
- the ServiceAccount's "Identity" in the generic inspector;
- the pod's "Identity" in the Pods lens;
- the store's "Authenticates as" in the External Secrets lens.

Each block reads when its inspector opens, and "Read again" re-reads it in every state. A reply is held with the request it answers (cluster, object and UID, and the read number) and shown only under it. A reading that disagrees with its declaration carries the hollow look-at mark, and a broken node component the fault mark; nothing reaches Needs you. A pod opens UID-pinned in the Pods lens; a ServiceAccount opens in the generic inspector.

**A deviation from §3.3: the GKE row on a cluster that is not GKE** (b4d5c96). The table said "Linked to G for impersonation" whenever the annotation is set. Only GKE's metadata server honours the link, so `Compare` now takes the platform the nodes show:
- nodes read, none GKE (`gce://`): "Declares G, which only GKE's metadata server honours; no node of this cluster is a GKE node, so the annotation does nothing here", in secondary text;
- nodes unread: the linked reading adds that whether this cluster is GKE is not known;
- GKE: as §3.3.

The lab-s5 captures found this on kind.

**Found during the build:**
- **The C2 side said its verdict twice** (b4d5c96). The evidence line now carries only the manager's and the observation's evidence.
- **Re-opening the resource list already shown left it empty** (d143b09, pre-existing, in the store). Choosing the open list again reset its rows, but the list loads only when its cluster or GVR changes, so it stayed on skeleton rows. The shown list's read is now kept.

**The lab** (scratchpad `lab-s5`, kind-klyx, fixtures per C6; every pod runs pause):
- **ServiceAccounts:** IRSA, Azure (client and tenant), GKE, one with no annotations for Pod Identity, and the stand-in controller's (IRSA).
- **Pods** in each webhook's documented shape:
  - IRSA current, a stale role, created without the env, a skipped container, partial, and both AWS mechanisms in one container;
  - Pod Identity with and without the pod label;
  - Azure current, and Azure without the use label;
  - a GKE-linked pod.
- **The stand-ins:**
  - an `eks-pod-identity-agent` DaemonSet in kube-system;
  - a `pod-identity-webhook` configuration, URL-served, fail-open, whose object selector matches nothing;
  - an ESO controller (`eso-standin`, `--controller-class=lab`) in the lab's namespace with only the chart's label. The ESO capability, keyed on external-secrets/external-secrets, therefore reads as before.
- **Stores:** AWS jwt with `role` and `additionalRoles`, AWS without auth (the controller), another class, static keys, legacy Azure with and without a store tenant, and GCP workload identity. No ESO controller runs, so each store is given a Ready status, and the fixtures add nothing to Needs you.
- **The needle** `S5LABneedle` sits in a non-identity env variable, in Pod Identity's URI user info, in Azure's authority-host query and in a static-key Secret.
- **Not in the lab:** the lab's ESO CRDs are v1beta1 (controller-gen 0.16.5) without `useAzureSDK` or `gcpServiceAccountEmail`. The new Azure SDK and the GSA email override are covered by `internal/identity`'s tests only. So are Auto Mode, Fargate and GKE nodes, as §8 said.

## 14. Gates

- **The four-way native smoke:** 170/170 in night-1440, night-1024, day-1440 and day-1024. The run was at b4d5c96, dirty with exactly 530be96's changes (the steps and these notes), clean. The labs (lab-s1b, lab-s1c, lab-s2b, lab-s3a) were re-applied within the hour, and lab-s5 was in place. The first run missed one step in the two 1440 modes, in the smoke's own step: with the inspector split, a pod selected earlier still showed its Identity block until the select intent landed, and the step read that block. Each identity step now waits for its subject's own block (530be96).
- **The parity gate:** passes, both stages, at 530be96 (run 1629e95381ce16bb; dirty only with this section, uncommitted while the gate ran). S5 adds no Observer part, so the gate is unchanged, as §8 said.
- **`go test -race ./...`:** ok.
- **golangci-lint v2.14.0:** 0 issues.
- **The frontend:**
  - the suite: 2197 passed;
  - tsc: clean;
  - eslint: 0 errors. The 3 warnings are pre-existing, in InstanceDetail and LogsPane.
- **The live captures** (scratchpad cap-s5, server mode at 530be96): 15 shots in each of the four themes and widths, 60 in all:
  - five ServiceAccounts (IRSA, Azure, Pod Identity, GKE, the stand-in controller's);
  - three pods (the stale role, Pod Identity with its label, Azure);
  - the seven stores.

  None scrolls sideways, none has a page error, and no identity block shows the lab's needle. The first probe found the two fixes in b4d5c96.
- **The load test:** not run. S5 adds no watch, no model and no evaluator, and its reads happen only when an inspector opens.

## 15. Implementation review 1: Codex's review of 58da867

Fixed in 1145032. Each finding has a regression, and every regression was mutation-checked. Codex's twelve probes pass; one probe call was adapted to `storesFor`'s new three-value signature.

| # | Finding | Fix |
|---|---|---|
| 1 (P1) | A projected token's audience crossed the reply raw | `Mechanism.Audience` is a `Value`: its exact bytes compare, and only a token-shaped form serialises. The test covers complete, wrong-audience and oversized chains through the pod, ServiceAccount and handler reports. |
| 2 | Only the ServiceAccount reply was bounded | The bound is measured over every final DTO with its counts. The Store reply drops handler pods from the end (`podsDropped`), keeping each group's ServiceAccount. The Pod reply drops container evidence and keeps the readings (`containersDropped`). A reply still over the bound says so in place of its report. |
| 3 | A stale store became a current subject; the inverse join ignored unresolved handlers | A store read that is not live and complete yields no store, so `StoreIdentity` reads "unreadable". The ServiceAccount's stores read "unread", with why. When a no-auth store's handler is not resolved, the inverse join reads "partial" and names the store. |
| 4 | gce:// proves Google Compute, not GKE | GKE now needs its node-pool label (`cloud.google.com/gke-nodepool`) or a gke-metadata-server pod on a gce:// node. Google Compute without either, or a node without a provider ID, is "not verified", with why. Not GKE needs every node to have a provider ID and none to be gce://. The metadata server's manager is the platform only on its pod (C2), and unknown otherwise. This corrects §13's deviation. |
| 5 | `gcpServiceAccountEmail` applied only with `serviceAccountRef` | The email is the requested principal after every federation base, as ESO applies it; the unknown base stays said. |
| 6 | Three chain readings overstated their evidence | A Windows path keeps its drive, and a path with no drive is on C:. A token entry past the bound in a partly read volume is unresolved; a volume that is not projected stays definite. Both AWS mechanisms are read by each one's own state: "configured" only when complete, and "neither complete" otherwise. |
| 7 | The wrong controller could be named | The last `--controller-class` wins, and "--" ends the flags, as pflag parses them. Only a Running pod's process handles stores: a Succeeded or Failed pod names nothing. A Pending or Unknown pod is said as "not running yet" and never counted; with no running pod, the handler is unresolved. |
| 8 | Unread or dropped data read as proven absence | A pod report says whether it was compared. An uncompared pod says so and shows its spec's mechanisms. Omission notices stand whatever the list's length, and "no pods run" needs a complete read with nothing dropped. The stores' unread and partial states are said. |
| 9 (P3) | Declared settings were not shown | The STS regional endpoint, AWS token expiration and Azure expiration are shown with their validation, and "no identity annotations" counts them. |

**The gates at 1145032:**
- **The four-way native smoke:** 170/170 in all four modes, clean, with the labs re-applied within the hour.
- **The live captures:** 60 shots (cap-s5r1), clean. On kind the GKE link now reads "does nothing here", and the irsa ServiceAccount names the one unjoined store (aws-other-class, whose class no running controller accepts).
- **`go test -race ./...`:** ok.
- **golangci-lint:** 0 issues.
- **The frontend:** 2202 passed, tsc clean, eslint 0 errors.
- **Parity:** not re-run. The fixes touch no Observer part.
