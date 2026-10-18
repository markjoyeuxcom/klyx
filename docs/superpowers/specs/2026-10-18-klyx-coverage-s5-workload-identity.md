# Coverage C2 and S5: management is not health; workload identity

Parent: `2026-10-16-klyx-coverage-roadmap.md` (accepted at 8281c6e), contract C2 and slice S5. C2 lands first, as the contract S5 uses.

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

**3.2 What a pod was given,** per container, from the pod's current spec:
- **The mechanism**, by its env and token volume together:
  - **IRSA:** `AWS_ROLE_ARN` and `AWS_WEB_IDENTITY_TOKEN_FILE`, and a mounted projected token volume whose path holds that file;
  - **Pod Identity:** `AWS_CONTAINER_CREDENTIALS_FULL_URI` and `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE`, and the token volume (audience `pods.eks.amazonaws.com`), plus the pod label when present;
  - **Azure:** `AZURE_CLIENT_ID`, `AZURE_FEDERATED_TOKEN_FILE` and a token volume with audience `api://AzureADTokenExchange` (name by prefix);
  - **partial:** some of a mechanism's env without the rest, or env without a token volume: "configured in the pod spec, not in the webhook's shape";
  - **none.**
- **Exclusions said,** each from its own evidence: a container named in `skip-containers` (comma on AWS, semicolon on Azure); an AWS opt-out label; an Azure pod without the exact `"true"` label; a `hostNetwork` pod on GKE.
- **Origin is never claimed.** The webhook, the pod template and another tool can all produce the same spec, so Klyx says what the pod has, not who wrote it.

**3.3 The comparison,** current against current, per container and adapter:

| Pod has | ServiceAccount declares now | Reading |
|---|---|---|
| IRSA with role X | role X | "Matches the ServiceAccount's role." |
| IRSA with role X | role Y | "Differs: the pod carries X; the ServiceAccount now declares Y. Pods take identity when created; one created before the change keeps X. A value set in the pod template also wins over the webhook." |
| IRSA with role X | no role | "Configured with X; the ServiceAccount declares no role now (removed later, set in the template, or a self-hosted webhook's ConfigMap)." |
| Pod Identity | anything | "EKS Pod Identity configured. Its association, and the role, live in AWS: not visible." With a declared IRSA role: "The webhook prefers Pod Identity over IRSA." |
| none | role Y | Each evidenced exclusion first (a skipped container, the opt-out label); otherwise "Not configured: the pod was created at T. A pod created before the annotation keeps none; when the annotation was set is not visible." |
| Azure client X | client X | "Matches." |
| Azure client X | client Y | "Differs" (as IRSA). |
| none, Azure declared | client Y | Without the pod label: "Not opted in: the ServiceAccount annotation alone does nothing; the pod needs `azure.workload.identity/use: "true"`." With it: as IRSA's "not configured". |
| (GKE) | linked GSA G | "Linked to G for impersonation; the IAM binding is not visible." Plus the node: whether it carries the metadata-server label, and its metadata-server pod. |

**Never inferred:** when an annotation changed (managedFields times cover other changes), whether a role trusts the cluster, whether an association exists, whether credentials work. The limits block says each, every time.

**3.4 The node side** (C2), for a pod using an AWS or GKE mechanism:
- **Pod Identity:** the pod's node. Auto Mode (its label): "built into EKS Auto Mode; health not directly observed". Otherwise the agent DaemonSet's pod on that node: ready, not ready (a fault the workload will feel, said as evidence, not a verdict), or none ("no Pod Identity agent pod on this node"). Fargate and Windows nodes: "EKS Pod Identity is not supported on this node" (from the node's labels).
- **GKE:** the node's metadata-server label, and its `gke-metadata-server` pod, ready or not.
- **The webhook** (`pod-identity-webhook`, `azure-wi-webhook-mutating-webhook-configuration`): present or not, how it is served, and a link to its Admission verdict when Service-backed (§2's table). Azure's `failurePolicy: Fail` is said: while its backend is unavailable, labelled pods cannot be created.

## 4. The ESO chain: who authenticates

**4.1 From a store** ("Authenticates as", in the ESO store inspector):
- **AWS:** `jwt.serviceAccountRef`: that ServiceAccount (the namespace resolved as §1 says), and whether it carries `eks.amazonaws.com/role-arn` (without it ESO refuses: "an IAM role must be associated", from its source) and that ESO mints its token itself, so Pod Identity cannot apply. `secretRef`: "static credentials from a Secret, not workload identity" (the Secret is never read). None: "the ESO controller's own identity", then the controller's chain (4.3).
- **Azure:** `WorkloadIdentity` with `serviceAccountRef`: that ServiceAccount and its client ID (or the `authSecretRef` one; both set is ESO's own error). Without it: the controller's own env, which must hold all three variables (4.3). `ManagedIdentity` and `ServicePrincipal`: named, not followed.
- **GCP:** `workloadIdentity.serviceAccountRef` or `workloadIdentityFederation.serviceAccountRef`: that ServiceAccount and its linked GSA if any. `secretRef`: static. None: the controller's ADC.
- **Validation ESO enforces, said when violated:** a namespaced store's `serviceAccountRef.namespace` must be empty or its own.
- **A ClusterSecretStore without a namespace** resolves per ExternalSecret: "each ExternalSecret's own namespace", never one guessed ServiceAccount.

**4.2 From a ServiceAccount:** the stores whose `serviceAccountRef` names it (a SecretStore in its namespace; a ClusterSecretStore naming its namespace, or naming none, said as "for ExternalSecrets in this namespace"). If it is the ESO controller's ServiceAccount, also every store with no auth: they authenticate as it.

**4.3 The controller's identity:** the controller Deployment (its pod template's ServiceAccount), and its running pods read as §3.2 does: what they were given, compared with that ServiceAccount (§3.3). An Azure store relying on the controller's env reads "the controller pods lack AZURE_CLIENT_ID (or the others): ESO will fail this store" only from a complete read of those pods.

**Version and adapters:** the stores come from the ESO lens's kinds (`external-secrets.io/v1`, or `v1beta1` where only that is served; the paths are the same). Other providers (Vault and the rest) are named "not read by Klyx's identity chain".

## 5. What Klyx shows

No new lens. Three inspector blocks, each read when its inspector opens and again on request:
- **ServiceAccount** (the generic inspector): "Identity":
  - its declarations per adapter;
  - its pods (up to 50, newest first, "+N not read" past that), each with its reading;
  - the node and webhook side;
  - the ESO stores that use it;
  - the limits.
- **Pod** (the Pods lens inspector): "Identity": its own containers' readings against its ServiceAccount's current declarations, and its node's side.
- **ESO store** (the External Secrets lens inspector): "Authenticates as": the subject, the chain to it, and a link to that ServiceAccount's identity.

**The limits, always said:**
- cloud trust, associations and permissions are not visible;
- identity is given at pod creation, and when an annotation changed is not visible;
- a value in the pod spec wins over the webhook, and Klyx cannot tell which wrote it;
- only EKS, Azure and GKE are read.

## 6. The source contract (C1) and the reads

| Read | How | Identity | Gaps |
|---|---|---|---|
| The ServiceAccount | GET, fenced by the UID the inspector holds (another UID: "replaced") | its UID | forbidden or failed: "unreadable", never "no identity" |
| Its pods | LIST in its namespace, `fieldSelector=spec.serviceAccountName=<name>`, limit 50 | each pod's UID | a continued list: "+N not read"; forbidden: unreadable |
| One pod (the Pods lens) | GET, fenced by UID; then its ServiceAccount (GET) | UIDs | as above |
| Nodes | the Observer's nodes model (labels, providerID), no read | node name | not live: "node evidence unverified" |
| The agent and the metadata server | the agent DaemonSet (GET) only when an AWS mechanism is in play; its pods and the metadata-server pods from the Observer's pods model | names | not read: unverified |
| The webhook configurations | the Observer's admission kinds (S3a) | their names | not read: unverified |
| The ESO stores | the Observer's ESO kinds, when ESO is installed | store UIDs | not complete: "stores unverified" |
| The ESO controller | the Deployment (GET) and its pods (LIST by its selector, limit 10) | UIDs | as above |

**Clock:** none. A read says when it was taken; a newer read replaces it, fenced by cluster, ServiceAccount or pod UID and request (the render-time fence of S3a's param check).

**Budget:** no new watches and no retained model. Per inspector read: at most 1 ServiceAccount GET, 1 pods LIST (50), 1 DaemonSet GET, 1 Deployment GET and 1 pods LIST (10).

## 7. Sensitive data

- **Env:** only the named identity variables (§3.2, and the region and STS ones) are kept, with their values: role ARNs, client and tenant IDs, token file paths and the credentials URI are identifiers, not secrets. Every other variable is dropped when the pod is parsed, name and value; a named variable set from a Secret or ConfigMap (`valueFrom`) reads "set from a Secret/ConfigMap (not read)".
- **Never read:** token contents (Klyx never reads a mounted file or calls TokenRequest), Secret values, `authSecretRef`/`secretRef` contents.
- **AI and chat:** nothing of S5 is sent; there is no Explain over these reads. The pod and ServiceAccount Explain targets keep their existing projections, which carry no env.

## 8. Tests, lab, parity, smoke

- **`internal/identity` (pure), unit:**
  - every row of §3.3 per adapter;
  - the env-pair rule, a partial mechanism, `valueFrom`, both separators, the exact Azure label, the old and hashed Azure volumes, the Windows token path;
  - Pod Identity precedence and the optional v0.6.15 label;
  - GKE `hostNetwork`;
  - the C2 table's rows, including an observed agent not ready on a managed cluster (never suppressed);
  - every ESO subject case of §4 (each provider, store kind and namespace rule, ESO's refusals).
- **`fleet`:** the reads with fake clients: the UID fence, the field selector, the cap, forbidden as unreadable.
- **appbridge:** the DTO, and a literal-needle test that no non-identity env value, Secret value or token content crosses.
- **Frontend:** the three blocks, their limits, and a fenced reply.
- **The lab (`lab-s5`, kind-klyx), fixtures per C6** (the webhooks are not lab components there; their output shapes are cited above):
  - ServiceAccounts with IRSA, Azure and GKE annotations;
  - pods in each §3.3 row, their env and volumes written in each webhook's documented shape (current, a stale role, created before the annotation, a skipped container, partial, an Azure pod without the label, Pod Identity with and without the pod label);
  - a stand-in `eks-pod-identity-agent` DaemonSet and a stand-in `pod-identity-webhook` configuration served by URL;
  - ESO stores of each auth shape on the ESO CRDs already in the lab.

  Not in the lab: an Auto Mode node, a Fargate node and GKE nodes. Relabelling kind's single node would change every other lab reading, so they are unit-tested.
- **Parity:** S5 adds no part, so the gate is unchanged.
- **Native smoke:** a lab ServiceAccount's Identity block, a stale-role pod, and an ESO store's subject.

## 9. Not in S5

- **AWS association reads** (an AWS API) and any cloud call: out of the programme.
- **KEDA authentication** (TriggerAuthentication): S9.
- **The controller-issue "managed" state and Karpenter on Auto Mode:** S10.
- **Other identity systems** (Vault Kubernetes auth, aad-pod-identity beyond naming `ManagedIdentity`, SPIFFE): named as not read.
- **Verbs:** none. Recreating pods to pick up an identity is the user's, through the existing restart verb.
