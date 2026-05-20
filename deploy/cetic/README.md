# CETIC custom deployment

Ce dossier contient les artefacts spécifiques à notre fork :

- **`Dockerfile.cetic`** — multi-stage build self-contained avec Go 1.25
  toolchain, ne dépend pas du build system `Task` upstream. Produit l'image
  `registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.5.0-cetic.X`.
- **`hcp-controller-deployment.yaml`** — manifest live du Deployment HCP
  controller dans `capi-hosted-control-plane-system` côté mgmt cluster CETIC.
  Sert de référence pour rejouer le deploy ou rollback.

## Pourquoi un fork

L'upstream teutonet/cluster-api-provider-hosted-control-plane v1.5.0 hardcode
les durations des Certificate cert-manager à 24h (leaves) / 48h (CAs) dans
`pkg/hostedcontrolplane/controller.go:101-102`. Sans intervention, tout
cluster HCP casse en split-brain TLS à T+24h-T+48h (bug upstream
[#87](https://github.com/teutonet/cluster-api-provider-hosted-control-plane/issues/87)).

Notre fork ajoute 2 env vars `CA_CERTIFICATE_DURATION` et
`CERTIFICATE_DURATION` qui permettent à l'opérateur d'override (defaults
inchangés = zero breaking change). On déploie avec les valeurs alignées
kubeadm/RKE2/EKS/GKE :

```yaml
env:
  - name: CA_CERTIFICATE_DURATION
    value: "87600h"  # 10 ans
  - name: CERTIFICATE_DURATION
    value: "8760h"   # 1 an
```

Une PR upstream sera ouverte pour merger ce patch dans le projet officiel
(à terme retirer le fork et revenir sur `ghcr.io/teutonet/...`).

## Build + push image

```bash
docker build -f deploy/cetic/Dockerfile.cetic \
  -t registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.5.0-cetic.X .
docker push registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.5.0-cetic.X
```

## Update deployment

```bash
KUBECONFIG=~/.kube/k8s-capi-mgmt-prod.yaml \
  kubectl -n capi-hosted-control-plane-system \
  set image deploy/capi-hosted-control-plane-controller-manager \
  manager=registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.5.0-cetic.X
```

## Rebase upstream

Quand teutonet sort une nouvelle version :

```bash
git fetch upstream
git checkout main
git merge upstream/main
git checkout feat/configurable-cert-durations
git rebase main
# Resolve conflicts (probably in controller.go + operator.go)
# Re-test : go build ./... && go test ./pkg/operator/... ./pkg/hostedcontrolplane/...
# Re-build image : docker build ... -t ...v1.X.Y-cetic.1 .
# Update Deployment image tag
```

## Pour retirer le fork (quand PR upstream mergée)

```bash
# Restore image officielle
KUBECONFIG=~/.kube/k8s-capi-mgmt-prod.yaml \
  kubectl -n capi-hosted-control-plane-system \
  set image deploy/capi-hosted-control-plane-controller-manager \
  manager=ghcr.io/teutonet/cluster-api-provider-hosted-control-plane:v1.X.Y

# Env vars restent (lus par le binaire officiel grâce à notre PR)
# Notre fork peut être archived
```
