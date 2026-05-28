# CLAUDE.cetic.md (CETIC fork-specific)

> Complète `CLAUDE.md` (upstream) avec les conventions internes au fork CETIC.

## Pourquoi ce fork

Upstream teutonet v1.6.0 a un **catch-22 au bootstrap** dans
`pkg/reconcilers/workload/reconciler.go` : la phase workload `coredns` peut
retourner `NotReady` (deployment 0/1 car aucun node) ; l'ancien code faisait
alors `return notReadyReason, nil` immédiatement, ce qui :

1. Empêchait la phase `konnectivity` suivante de tourner
2. Empêchait `Status.Initialization.ControlPlaneInitialized = true` de
   s'exécuter (cette ligne est APRÈS la loop)

Conséquence : Cluster condition `ControlPlaneInitialized=False` → le CAPI
bootstrap controller refuse de générer le dataSecret → workers
`WaitingForBootstrapData` à jamais → CoreDNS attend des nodes qui attendent
le bootstrap qui attend CoreDNS. Catch-22 total sur tout nouveau cluster.

`ccks-tech-tools-dev` (bootstrappé en v1.5.0 ou avant) survit grâce à son
flag `controlPlaneInitialized=true` persisté avant l'upgrade v1.6.0.

**Notre patch** (`pkg/reconcilers/workload/reconciler.go`) :
- Capture le PREMIER `notReadyReason` dans `firstNotReadyReason` au lieu de
  return immédiat
- Continue la loop pour reconciler toutes les phases (konnectivity inclus)
- Set `ControlPlaneInitialized=true` à la fin
- Retourne `firstNotReadyReason` au caller (préservation de la sémantique
  de requeue)

Le flag `WorkloadCoreDNSReady=False` reste correct (suivi de l'état réel),
mais ne bloque plus l'init. Quand le 1er worker join, CoreDNS schedule,
condition repasse `True`, idempotent.

## Note sur l'ancien patch certs (retiré en v1.6.0-cetic.1)

Le fork CETIC précédent (`feat/configurable-cert-durations`, basé sur
upstream v1.5.0) ajoutait 2 env vars `CA_CERTIFICATE_DURATION` /
`CERTIFICATE_DURATION` pour contourner le hardcode `24h`/`48h` des certs
cert-manager (bug upstream
[#87](https://github.com/teutonet/cluster-api-provider-hosted-control-plane/issues/87)).
**Upstream v1.6.0 corrige ce bug** — patch retiré, plus besoin.

## Workflow de release CETIC

À chaque modif du code Go (rebase upstream OU patch interne) :

```bash
# 1. Build + push l'image avec tag CETIC incrémenté
docker build -f deploy/cetic/Dockerfile.cetic \
  -t registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.6.0-cetic.X .
docker push registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.6.0-cetic.X

# 2. Update le Deployment HCP controller mgmt-side
KUBECONFIG=~/.kube/ccp-capi-mgmt-prod.yaml \
  kubectl -n capi-hosted-control-plane-system \
  set image deploy/capi-hosted-control-plane-controller-manager \
  manager=registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.6.0-cetic.X

# 3. Wait Deployment Ready
KUBECONFIG=~/.kube/ccp-capi-mgmt-prod.yaml \
  kubectl -n capi-hosted-control-plane-system rollout status \
  deploy/capi-hosted-control-plane-controller-manager
```

Convention tag : `v<UPSTREAM>-cetic.<N>` (ex. `v1.6.0-cetic.2` =
2e build CETIC basé sur upstream v1.6.0).

## Rebase upstream

Quand teutonet sort une nouvelle release :

```bash
git fetch upstream                          # upstream = teutonet/...
git checkout main && git merge upstream/main
git checkout -b cetic/v<NEW> upstream/v<NEW>
git cherry-pick <sha-du-commit-deploy-cetic>    # deploy/cetic/ files
# Re-apply manuellement le patch reconciler.go workload si nécessaire
# (vérifier d'abord si upstream a fixé le catch-22 ; si oui, drop ce patch)
# Re-test :
go vet ./... && go test ./pkg/hostedcontrolplane/...
# Re-build image
docker build -f deploy/cetic/Dockerfile.cetic \
  -t registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v<NEW>-cetic.1 .
```

## Branches

| Branch | Rôle |
|---|---|
| `main` | Mirror passive de `teutonet/main`, juste pour rebase |
| `cetic/v1.6.0` | **Branche active du fork** — contient le patch CoreDNS + deploy/cetic/. C'est ce que notre Deployment prod utilise. |
| `feat/configurable-cert-durations` | LEGACY — branche du fork v1.5.0 (patch certs). Conservée pour archive. |

## Setup git remote

```bash
git remote add upstream https://github.com/teutonet/cluster-api-provider-hosted-control-plane.git
git remote -v
# origin    = cetic-group/cluster-api-provider-hosted-control-plane (notre fork)
# upstream  = teutonet/cluster-api-provider-hosted-control-plane    (officiel)
```

## Politique

- **Pas de PR upstream** — on maintient downstream only et on sync au besoin
- **NE PAS commit dans `main`** — `main` doit rester miroir de `upstream/main`
- Toutes nos modifs vivent sur `cetic/v<UPSTREAM>` (branche dédiée par
  version upstream majeure)
