#!/usr/bin/env bash
# Deploys the controller image into the e2e cluster with deploy/controller.yaml
# (exercising its RBAC) and runs a 3-wave WavePlan through it.
set -euo pipefail
cd "$(dirname "$0")/.."
: "${KUBECONFIG:?}" "${CLUSTER:?}"

docker build -q -t ghcr.io/chadidridi/waveform:e2e .
kind load docker-image ghcr.io/chadidridi/waveform:e2e --name "$CLUSTER"
sed 's|waveform:latest|waveform:e2e|' deploy/controller.yaml | kubectl apply -f -
kubectl -n waveform-system rollout restart deploy/waveform-controller # pick up a re-loaded :e2e image
kubectl -n waveform-system rollout status deploy/waveform-controller --timeout=120s

for s in db api web; do
  kubectl -n shop patch rollout "$s" --type=merge -p \
    '{"spec":{"template":{"spec":{"containers":[{"name":"app","image":"registry.k8s.io/pause:3.10","env":[{"name":"VERSION","value":"incluster"}]}]}}}}'
done
for i in $(seq 90); do
  [ "$(kubectl -n shop get rollouts -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' | grep -c Paused)" = 3 ] && break
  sleep 2
done

kubectl -n shop delete waveplan incluster --ignore-not-found
kubectl apply -f - <<'YAML'
apiVersion: waveform.chadidridi.github.io/v1alpha1
kind: WavePlan
metadata: {name: incluster, namespace: shop}
spec:
  gate: {bake: 2s}
  waves:
    - index: 1
      steps: [{service: db, kind: additive, rollout: {name: db}}]
    - index: 2
      steps: [{service: api, kind: additive, after: [db], rollout: {name: api}}]
    - index: 3
      steps: [{service: web, kind: additive, after: [api], rollout: {name: web}}]
YAML

for i in $(seq 120); do
  phase=$(kubectl -n shop get waveplan incluster -o jsonpath='{.status.phase}')
  case "$phase" in Succeeded) break ;; RolledBack|Halted) break ;; esac
  sleep 2
done
kubectl -n shop get waveplan incluster -o wide
kubectl -n waveform-system logs deploy/waveform-controller | tail -n 20
[ "$phase" = Succeeded ] || { echo "in-cluster WavePlan ended $phase"; exit 1; }
echo "in-cluster controller: OK"
