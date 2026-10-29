#!/bin/zsh
# Coverage S5 lab fixtures on kind-klyx (note §8, fixtures per C6). The
# identity webhooks and the Pod Identity agent are not lab components: the
# pods below carry each webhook's documented output shape (env, projected
# token volume, mount), written by hand. Every pod runs pause; nothing in
# the lab authenticates anywhere. The stand-in pod-identity-webhook
# configuration is URL-served, fail-open and selects no object, so it never
# admits anything. The stand-in ESO controller lives in this namespace with
# only the chart's label, so the ESO capability (external-secrets/
# external-secrets) reads as before. The needle S5LABneedle sits in a
# non-identity env variable, in URL user info, in a URL query and in a
# static-key Secret: none may reach an identity block. Idempotent; every
# write targets kind-klyx only. Pods are immutable: after changing one,
# delete the lab's pods (-l klyx.dev/lab=s5) before re-applying.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
N=S5LABneedle
NS=klyx-identity-lab
ROLE=arn:aws:iam::111122223333:role/lab-current
OLD=arn:aws:iam::111122223333:role/lab-old
CLIENT=11111111-2222-3333-4444-555555555555
TENANT=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
PAUSE='image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent'

# The webhooks' shapes (aws/amazon-eks-pod-identity-webhook, EKS Pod
# Identity, Azure/azure-workload-identity), as S5 §3.2 cites them.
IRSA_ENV="{name: AWS_ROLE_ARN, value: @ROLE@}, {name: AWS_WEB_IDENTITY_TOKEN_FILE, value: /var/run/secrets/eks.amazonaws.com/serviceaccount/token}, {name: AWS_STS_REGIONAL_ENDPOINTS, value: regional}, {name: AWS_REGION, value: eu-west-1}"
IRSA_MOUNT='{name: aws-iam-token, mountPath: /var/run/secrets/eks.amazonaws.com/serviceaccount, readOnly: true}'
IRSA_VOL='{name: aws-iam-token, projected: {sources: [{serviceAccountToken: {audience: sts.amazonaws.com, expirationSeconds: 86400, path: token}}]}}'
PI_ENV="{name: AWS_CONTAINER_CREDENTIALS_FULL_URI, value: '@URI@'}, {name: AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE, value: /var/run/secrets/pods.eks.amazonaws.com/serviceaccount/eks-pod-identity-token}"
PI_MOUNT='{name: eks-pod-identity-token, mountPath: /var/run/secrets/pods.eks.amazonaws.com/serviceaccount, readOnly: true}'
PI_VOL='{name: eks-pod-identity-token, projected: {sources: [{serviceAccountToken: {audience: pods.eks.amazonaws.com, expirationSeconds: 86400, path: eks-pod-identity-token}}]}}'
AZ_ENV="{name: AZURE_CLIENT_ID, value: $CLIENT}, {name: AZURE_TENANT_ID, value: $TENANT}, {name: AZURE_FEDERATED_TOKEN_FILE, value: /var/run/secrets/azure/tokens/azure-identity-token}, {name: AZURE_AUTHORITY_HOST, value: '@HOST@'}"
AZ_MOUNT='{name: azure-identity-token, mountPath: /var/run/secrets/azure/tokens, readOnly: true}'
AZ_VOL='{name: azure-identity-token, projected: {sources: [{serviceAccountToken: {audience: 'api://AzureADTokenExchange', expirationSeconds: 3600, path: azure-identity-token}}]}}'

pod() { # name sa labels annotations env mounts volumes
	print -r -- "---
apiVersion: v1
kind: Pod
metadata: {name: $1, namespace: $NS, labels: {klyx.dev/lab: s5$3}, annotations: {$4}}
spec:
  serviceAccountName: $2
  terminationGracePeriodSeconds: 1
  containers: [{name: app, $PAUSE, env: [$5], volumeMounts: [$6]}]
  volumes: [$7]"
}

{
print -r -- "apiVersion: v1
kind: ServiceAccount
metadata: {name: irsa, namespace: $NS, annotations: {eks.amazonaws.com/role-arn: $ROLE}}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: azure, namespace: $NS, annotations: {azure.workload.identity/client-id: $CLIENT, azure.workload.identity/tenant-id: $TENANT}}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: podid, namespace: $NS}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: gke, namespace: $NS, annotations: {iam.gke.io/gcp-service-account: lab-gsa@lab-proj.iam.gserviceaccount.com}}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: eso-controller, namespace: $NS, annotations: {eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/lab-eso}}
---
apiVersion: v1
kind: Secret
metadata: {name: lab-aws-keys, namespace: $NS}
stringData: {access-key: $N, secret-access-key: $N}"
# IRSA: current, a stale role, created before the annotation (no env), a
# skipped container, partial (no token file), and both AWS mechanisms.
pod irsa-current irsa '' '' "${IRSA_ENV//@ROLE@/$ROLE}, {name: LAB_TOKEN, value: $N}" "$IRSA_MOUNT" "$IRSA_VOL"
pod irsa-stale irsa '' '' "${IRSA_ENV//@ROLE@/$OLD}" "$IRSA_MOUNT" "$IRSA_VOL"
pod irsa-before irsa '' '' '' '' ''
pod irsa-skipped irsa '' 'eks.amazonaws.com/skip-containers: app' '' '' ''
pod irsa-partial irsa '' '' "{name: AWS_ROLE_ARN, value: $ROLE}" '' ''
pod irsa-both irsa '' '' "${IRSA_ENV//@ROLE@/$ROLE}, ${PI_ENV//@URI@/http://169.254.170.23/v1/credentials}" "$IRSA_MOUNT, $PI_MOUNT" "$IRSA_VOL, $PI_VOL"
# Pod Identity, with and without the v0.6.15 pod label; one URI carries
# user info (never shown).
pod podid-labelled podid ', eks.amazonaws.com/pod-identity: enabled' '' "${PI_ENV//@URI@/http://lab:$N@169.254.170.23/v1/credentials}" "$PI_MOUNT" "$PI_VOL"
pod podid-plain podid '' '' "${PI_ENV//@URI@/http://169.254.170.23/v1/credentials}" "$PI_MOUNT" "$PI_VOL"
# Azure: current (its authority host carries a query, never shown), and a
# pod without the use label.
pod azure-current azure ', azure.workload.identity/use: "true"' '' "${AZ_ENV//@HOST@/https://login.microsoftonline.com/?lab=$N}" "$AZ_MOUNT" "$AZ_VOL"
pod azure-nolabel azure '' '' '' '' ''
pod gke-pod gke '' '' '' '' ''
# The stand-in Pod Identity agent and webhook configuration.
print -r -- "---
apiVersion: apps/v1
kind: DaemonSet
metadata: {name: eks-pod-identity-agent, namespace: kube-system, labels: {klyx.dev/lab: s5}}
spec:
  selector: {matchLabels: {app.kubernetes.io/name: eks-pod-identity-agent}}
  template:
    metadata: {labels: {app.kubernetes.io/name: eks-pod-identity-agent}}
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: eks-pod-identity-agent, $PAUSE}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata: {name: pod-identity-webhook, labels: {klyx.dev/lab: s5}}
webhooks:
- name: iam-for-pods.amazonaws.com
  failurePolicy: Ignore
  sideEffects: None
  admissionReviewVersions: [v1beta1]
  timeoutSeconds: 5
  clientConfig: {url: 'https://127.0.0.1:23443/mutate'}
  objectSelector: {matchLabels: {klyx.dev/lab-never: \"true\"}}
  rules: [{operations: [CREATE], apiGroups: [''], apiVersions: [v1], resources: [pods]}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: eso-standin, namespace: $NS, labels: {app.kubernetes.io/name: external-secrets}}
spec:
  replicas: 1
  selector: {matchLabels: {app.kubernetes.io/name: external-secrets}}
  template:
    metadata: {labels: {app.kubernetes.io/name: external-secrets}}
    spec:
      serviceAccountName: eso-controller
      terminationGracePeriodSeconds: 1
      containers: [{name: external-secrets, $PAUSE, args: [--controller-class=lab],
        env: [${IRSA_ENV//@ROLE@/arn:aws:iam::111122223333:role/lab-eso}], volumeMounts: [$IRSA_MOUNT]}]
      volumes: [$IRSA_VOL]
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: aws-jwt, namespace: $NS}
spec:
  provider:
    aws:
      service: SecretsManager
      region: eu-west-1
      role: arn:aws:iam::111122223333:role/lab-target
      additionalRoles: [arn:aws:iam::111122223333:role/lab-hop]
      auth: {jwt: {serviceAccountRef: {name: irsa}}}
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: aws-controller, namespace: $NS}
spec:
  provider: {aws: {service: SecretsManager, region: eu-west-1}}
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: aws-other-class, namespace: $NS}
spec:
  controller: other
  provider: {aws: {service: SecretsManager, region: eu-west-1}}
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: aws-static, namespace: $NS}
spec:
  provider:
    aws:
      service: SecretsManager
      region: eu-west-1
      auth: {secretRef: {accessKeyIDSecretRef: {name: lab-aws-keys, key: access-key}, secretAccessKeySecretRef: {name: lab-aws-keys, key: secret-access-key}}}
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: azure-wi, namespace: $NS}
spec:
  provider: {azurekv: {authType: WorkloadIdentity, vaultUrl: 'https://lab.vault.azure.net', serviceAccountRef: {name: azure}}}
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: azure-wi-tenant, namespace: $NS}
spec:
  provider: {azurekv: {authType: WorkloadIdentity, vaultUrl: 'https://lab.vault.azure.net', tenantId: $TENANT, serviceAccountRef: {name: azure}}}
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata: {name: gcp-wi, namespace: $NS}
spec:
  provider: {gcpsm: {projectID: lab-proj, auth: {workloadIdentity: {clusterLocation: europe-west1, clusterName: lab, clusterProjectID: lab-proj, serviceAccountRef: {name: gke}}}}}"
} > ${0:A:h}/preview.yaml
exit 0

# No ESO controller runs in the lab: each store is given a Ready status so
# the S5 fixtures add nothing to Needs you.
T=$(date -u +%Y-%m-%dT%H:%M:%SZ)
for s in aws-jwt aws-controller aws-other-class aws-static azure-wi azure-wi-tenant gcp-wi; do
	k patch secretstore $s -n $NS --subresource=status --type=merge \
		-p "{\"status\":{\"capabilities\":\"ReadWrite\",\"conditions\":[{\"type\":\"Ready\",\"status\":\"True\",\"reason\":\"Valid\",\"message\":\"store validated\",\"lastTransitionTime\":\"$T\"}]}}" >/dev/null
done
k wait --for=condition=Ready pod -l klyx.dev/lab=s5 -n $NS --timeout=90s >/dev/null
k rollout status deploy/eso-standin -n $NS --timeout=90s >/dev/null
k rollout status ds/eks-pod-identity-agent -n kube-system --timeout=90s >/dev/null
echo "lab-s5 applied"
