#!/bin/zsh
# Dumps the S9 lab's objects as JSON into the state directory, s9-read/<stamp>/.
source ${0:A:h}/../../lib.sh && target
k() { t 60 kube "$@"; }
D=$dir/s9-read/${1:-now}; mkdir -p $D
for r in scaledobjects scaledjobs triggerauthentications hpa jobs deployments statefulsets events; do k -n klyx-s9-lab get $r -o json > $D/$r.json; done
k get clustertriggerauthentications -o json > $D/clustertriggerauthentications.json
k -n keda get deploy -o json > $D/keda-deploy.json
k get apiservice v1beta1.external.metrics.k8s.io -o json > $D/apiservice.json
k get crd scaledobjects.keda.sh scaledjobs.keda.sh triggerauthentications.keda.sh clustertriggerauthentications.keda.sh -o json > $D/crds.json
echo $D
