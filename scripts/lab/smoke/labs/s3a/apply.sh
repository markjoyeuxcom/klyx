#!/bin/zsh
# Coverage S3a lab fixtures on kind-klyx. Every webhook targets a group and
# resource that do not exist (lab.klyx.dev/widgets), and every policy
# binding matches no namespace, so nothing in the lab is ever admitted
# through them. The needle S3aLABneedle sits in a webhook URL's path (user
# info is rejected by the API) and in a CEL expression that draws a
# type-checking warning. Idempotent; every write targets kind-klyx only.
set -e
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
N=S3aLABneedle
NS=klyx-admission-lab
RULES='[{operations: [CREATE], apiGroups: [lab.klyx.dev], apiVersions: [v1], resources: [widgets]}]'
hook() { # name failurePolicy clientConfig
	printf -- '- name: %s\n  failurePolicy: %s\n  sideEffects: None\n  admissionReviewVersions: [v1]\n  timeoutSeconds: 5\n  clientConfig: %s\n  rules: %s\n' "$1" "$2" "$3" "$RULES"
}
k create namespace $NS --dry-run=client -o yaml | k apply -f - >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: hook-ok, namespace: $NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: hook-ok}}
  template:
    metadata: {labels: {app: hook-ok}}
    spec:
      terminationGracePeriodSeconds: 1
      containers: [{name: hook, image: registry.k8s.io/pause:3.10, imagePullPolicy: IfNotPresent}]
---
apiVersion: v1
kind: Service
metadata: {name: hook-ok, namespace: $NS}
spec: {selector: {app: hook-ok}, ports: [{name: https, port: 443, targetPort: 8443}]}
---
apiVersion: v1
kind: Service
metadata: {name: hook-noready, namespace: $NS}
spec: {selector: {app: nothing-matches}, ports: [{name: https, port: 443, targetPort: 8443}]}
---
apiVersion: v1
kind: Service
metadata: {name: hook-headless, namespace: $NS}
spec: {clusterIP: None, selector: {app: hook-ok}, ports: [{name: https, port: 443, targetPort: 8443}]}
---
apiVersion: v1
kind: Service
metadata: {name: hook-external, namespace: $NS}
spec: {type: ExternalName, externalName: hooks.example.com}
---
apiVersion: v1
kind: Service
metadata: {name: hook-otherport, namespace: $NS}
spec: {selector: {app: hook-ok}, ports: [{name: https, port: 8443, targetPort: 8443}]}
YAML
k -n $NS rollout status deployment/hook-ok --timeout=120s >/dev/null
svc() { printf '{service: {namespace: %s, name: %s, port: 443}}' "$NS" "$1"; }
{
	printf 'apiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata: {name: lab-fail-missing}\nwebhooks:\n'; hook missing.lab.klyx.dev Fail "$(svc hook-gone)"
	printf -- '---\napiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata: {name: lab-fail-noready}\nwebhooks:\n'; hook noready.lab.klyx.dev Fail "$(svc hook-noready)"
	printf -- '---\napiVersion: admissionregistration.k8s.io/v1\nkind: MutatingWebhookConfiguration\nmetadata: {name: lab-open-otherport}\nwebhooks:\n'; hook otherport.lab.klyx.dev Ignore "$(svc hook-otherport)"
	printf -- '---\napiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata: {name: lab-headless}\nwebhooks:\n'; hook headless.lab.klyx.dev Fail "$(svc hook-headless)"
	printf -- '---\napiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata: {name: lab-external}\nwebhooks:\n'; hook external.lab.klyx.dev Fail "$(svc hook-external)"
	printf -- '---\napiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata: {name: lab-url}\nwebhooks:\n'; hook url.lab.klyx.dev Ignore "{url: \"https://hooks.lab.klyx.dev/$N\"}"
	printf -- '---\napiVersion: admissionregistration.k8s.io/v1\nkind: ValidatingWebhookConfiguration\nmetadata: {name: lab-healthy}\nwebhooks:\n'; hook healthy.lab.klyx.dev Fail "$(svc hook-ok)"; hook second.lab.klyx.dev Ignore "$(svc hook-ok)"
} | k apply -f - >/dev/null
k apply -f - >/dev/null <<YAML
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: lab-unbound}
spec: {matchConstraints: {resourceRules: $RULES}, validations: [{expression: "true"}]}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: lab-params}
spec:
  paramKind: {apiVersion: lab.klyx.dev/v1, kind: Widget}
  matchConstraints: {resourceRules: $RULES}
  validations: [{expression: "true"}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: lab-params-b}
spec:
  policyName: lab-params
  validationActions: [Deny]
  paramRef: {name: limits, namespace: $NS, parameterNotFoundAction: Deny}
  matchResources: {namespaceSelector: {matchLabels: {klyx.dev/lab: none}}}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: lab-warning}
spec:
  failurePolicy: Ignore
  matchConstraints: {resourceRules: [{operations: [CREATE], apiGroups: [""], apiVersions: [v1], resources: [configmaps]}]}
  validations: [{expression: "object.$N == 1"}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: lab-warning-b}
spec:
  policyName: lab-warning
  validationActions: [Audit]
  matchResources: {namespaceSelector: {matchLabels: {klyx.dev/lab: none}}}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: lab-healthy}
spec: {failurePolicy: Fail, matchConstraints: {resourceRules: $RULES}, validations: [{expression: "true"}]}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: lab-healthy-b}
spec: {policyName: lab-healthy, validationActions: [Deny], matchResources: {namespaceSelector: {matchLabels: {klyx.dev/lab: none}}}}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: lab-dangling-b}
spec: {policyName: lab-gone, validationActions: [Deny], matchResources: {namespaceSelector: {matchLabels: {klyx.dev/lab: none}}}}
---
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingAdmissionPolicy
metadata: {name: lab-mutating}
spec:
  failurePolicy: Ignore
  reinvocationPolicy: Never
  matchConstraints: {resourceRules: $RULES}
  mutations: [{patchType: ApplyConfiguration, applyConfiguration: {expression: "Object{metadata: Object.metadata{labels: {\"lab\": \"s3a\"}}}"}}]
---
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingAdmissionPolicyBinding
metadata: {name: lab-mutating-b}
spec: {policyName: lab-mutating, matchResources: {namespaceSelector: {matchLabels: {klyx.dev/lab: none}}}}
YAML
echo "lab-s3a applied at $(date -u +%Y-%m-%dT%H:%M:%SZ)"
