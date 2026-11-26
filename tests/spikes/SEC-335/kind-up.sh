#!/bin/sh
export KUBECONFIG=/dev/null
export KIND_EXPERIMENTAL_PROVIDER=podman
export KIND_EXPERIMENTAL_PODMAN_NETWORK=podman
T=/home/openova/.cache/ns-scratch/SEC-335-tools
kind create cluster --name sec335 --kubeconfig $T/kind.kubeconfig --wait 240s > $T/kind-up.log 2>&1
echo "kind exit=$?"
grep -v "level=warning" $T/kind-up.log | tail -15
kubectl --kubeconfig $T/kind.kubeconfig get nodes -o wide 2>&1 | tail -3
kubectl --kubeconfig $T/kind.kubeconfig get pods -A 2>&1 | tail -15
