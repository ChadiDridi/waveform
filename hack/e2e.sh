#!/usr/bin/env bash
# Creates a throwaway kind cluster with Argo Rollouts and the WavePlan CRD,
# runs the e2e tests, then deletes the cluster (KEEP=1 keeps it).
# Uses its own kubeconfig file; your ~/.kube/config is not touched.
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${CLUSTER:-waveform-e2e}
ROLLOUTS_VERSION=${ROLLOUTS_VERSION:-v1.10.1}
WORK=${WORK:-$(mktemp -d)}
export KUBECONFIG="$WORK/kubeconfig"

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --wait 120s
else
  kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG"
fi
cleanup() { [ "${KEEP:-0}" = 1 ] || kind delete cluster --name "$CLUSTER"; }
trap cleanup EXIT

kubectl create namespace argo-rollouts --dry-run=client -o yaml | kubectl apply -f -
kubectl apply --server-side -n argo-rollouts -f "https://github.com/argoproj/argo-rollouts/releases/download/${ROLLOUTS_VERSION}/install.yaml"
kubectl apply -f deploy/crd.yaml
kubectl create namespace shop --dry-run=client -o yaml | kubectl apply -f -
kubectl -n argo-rollouts rollout status deploy/argo-rollouts --timeout=180s
kubectl wait --for=condition=Established crd/waveplans.waveform.chadidridi.github.io --timeout=60s

go test -tags e2e -count=1 -v -timeout 20m ./e2e/
CLUSTER="$CLUSTER" ./hack/e2e-incluster.sh
