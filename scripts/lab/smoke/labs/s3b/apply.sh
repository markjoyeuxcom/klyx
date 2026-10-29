#!/bin/zsh
# The S3b lab on kind-klyx (coverage S3b §8): in a labelled namespace, a
# validating webhook that denies a labelled ConfigMap, backed by a real
# Service (agnhost webhook's /always-deny); a fail-closed webhook whose
# Service has no endpoints (a call failure) on a labelled Kustomization; and
# a ValidatingAdmissionPolicy with a binding that freezes scaling. Each is
# scoped by its object's label or the lab namespace, so nothing else is
# refused. The TLS pair is the lab's own, generated here.
set -e
source ${0:A:h}/common.sh && target
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k label namespace $NS klyx.dev/lab=s3b --overwrite >/dev/null
NEWCERTS=0
if [ ! -f $CERTS/tls.crt ]; then
  NEWCERTS=1
  mkdir -p $CERTS
  openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=klyx-s3b-lab-ca" -keyout $CERTS/ca.key -out $CERTS/ca.crt >/dev/null 2>&1
  openssl req -newkey rsa:2048 -nodes -subj "/CN=lab-hook.$NS.svc" -keyout $CERTS/tls.key -out $CERTS/tls.csr >/dev/null 2>&1
  printf 'subjectAltName=DNS:lab-hook.%s.svc,DNS:lab-hook.%s.svc.cluster.local\n' $NS $NS > $CERTS/ext.cnf
  openssl x509 -req -in $CERTS/tls.csr -CA $CERTS/ca.crt -CAkey $CERTS/ca.key -CAcreateserial -days 30 -extfile $CERTS/ext.cnf -out $CERTS/tls.crt >/dev/null 2>&1
fi
k -n $NS create secret tls lab-hook-tls --cert=$CERTS/tls.crt --key=$CERTS/tls.key --dry-run=client -o yaml | k apply -f - >/dev/null
CA=$(base64 < $CERTS/ca.crt | tr -d '\n')
# the fixtures first, so the Flux controller has handled its Kustomization
# before the unreachable webhook would refuse it
k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: ConfigMap
metadata: {name: lab-webhook, namespace: $NS, labels: {klyx.dev/lab: s3b, klyx.dev/s3b: webhook}}
data: {note: "Klyx's YAML apply to this object is refused by the lab webhook"}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: lab-frozen, namespace: $NS, labels: {klyx.dev/lab: s3b}}
spec:
  replicas: 1
  selector: {matchLabels: {app: lab-frozen}}
  template:
    metadata: {labels: {app: lab-frozen}}
    spec:
      terminationGracePeriodSeconds: 0
      containers: [{name: pause, image: registry.k8s.io/e2e-test-images/busybox:1.36.1-1, imagePullPolicy: IfNotPresent, command: [sleep, "1000000"]}]
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: lab-unreachable, namespace: $NS, labels: {klyx.dev/lab: s3b, klyx.dev/s3b: unreachable}}
spec:
  interval: 1h
  path: ./lab
  prune: false
  suspend: true
  sourceRef: {kind: GitRepository, name: lab-none}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: lab-hook, namespace: $NS, labels: {klyx.dev/lab: s3b}}
spec:
  replicas: 1
  selector: {matchLabels: {app: lab-hook}}
  template:
    metadata: {labels: {app: lab-hook}}
    spec:
      containers:
      - name: webhook
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        imagePullPolicy: IfNotPresent
        args: [webhook, --tls-cert-file=/certs/tls.crt, --tls-private-key-file=/certs/tls.key, --port=8443]
        ports: [{containerPort: 8443}]
        volumeMounts: [{name: certs, mountPath: /certs, readOnly: true}]
      volumes: [{name: certs, secret: {secretName: lab-hook-tls}}]
---
apiVersion: v1
kind: Service
metadata: {name: lab-hook, namespace: $NS, labels: {klyx.dev/lab: s3b}}
spec:
  selector: {app: lab-hook}
  ports: [{port: 443, targetPort: 8443}]
---
apiVersion: v1
kind: Service
metadata: {name: lab-hook-gone, namespace: $NS, labels: {klyx.dev/lab: s3b}}
spec:
  selector: {app: lab-hook-nothing-runs-this}
  ports: [{port: 443, targetPort: 8443}]
YAML
# new certificates: the running webhook still serves the old ones (2026-10-28)
if [ $NEWCERTS = 1 ]; then k -n $NS rollout restart deployment/lab-hook >/dev/null; fi
k -n $NS rollout status deployment/lab-hook --timeout=180s >/dev/null
k -n $NS rollout status deployment/lab-frozen --timeout=180s >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata: {name: klyx-s3b-deny, labels: {klyx.dev/lab: s3b}}
webhooks:
- name: deny.s3b.klyx.dev
  admissionReviewVersions: [v1]
  sideEffects: None
  failurePolicy: Fail
  timeoutSeconds: 5
  clientConfig: {service: {namespace: $NS, name: lab-hook, path: /always-deny}, caBundle: $CA}
  objectSelector: {matchLabels: {klyx.dev/s3b: webhook}}
  rules: [{operations: [UPDATE], apiGroups: [""], apiVersions: [v1], resources: [configmaps], scope: Namespaced}]
- name: unreachable.s3b.klyx.dev
  admissionReviewVersions: [v1]
  sideEffects: None
  failurePolicy: Fail
  timeoutSeconds: 5
  clientConfig: {service: {namespace: $NS, name: lab-hook-gone, path: /always-deny}, caBundle: $CA}
  objectSelector: {matchLabels: {klyx.dev/s3b: unreachable}}
  rules: [{operations: [UPDATE], apiGroups: [kustomize.toolkit.fluxcd.io], apiVersions: [v1], resources: [kustomizations], scope: Namespaced}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: klyx-s3b-freeze, labels: {klyx.dev/lab: s3b}}
spec:
  failurePolicy: Fail
  matchConstraints:
    namespaceSelector: {matchLabels: {klyx.dev/lab: s3b}}
    resourceRules: [{operations: [UPDATE], apiGroups: [apps], apiVersions: [v1], resources: [deployments/scale]}]
  validations: [{expression: "false", message: "scaling is frozen in the S3b lab"}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: klyx-s3b-freeze-binding, labels: {klyx.dev/lab: s3b}}
spec:
  policyName: klyx-s3b-freeze
  validationActions: [Deny]
  matchResources:
    namespaceSelector: {matchLabels: {klyx.dev/lab: s3b}}
YAML
# the refusals, as the server answers them
until_ok "the webhook refuses the labelled ConfigMap" sh -c "! $kubesh -n $NS label configmap lab-webhook probe=\$(date +%s) --overwrite 2>&1 | grep -q . || $kubesh -n $NS label configmap lab-webhook probe=x --overwrite 2>&1 | grep -q 'denied the request'"
until_ok "the policy freezes scaling" sh -c "$kubesh -n $NS scale deployment lab-frozen --replicas=2 2>&1 | grep -q 'klyx-s3b-freeze'"
until_ok "the unreachable webhook fails the Kustomization" sh -c "$kubesh -n $NS patch kustomization lab-unreachable --type merge -p '{\"spec\":{\"suspend\":false}}' 2>&1 | grep -q 'failed calling webhook'"
echo "lab-s3b applied"
