# CLAUDE.cetic.md (CETIC fork-specific)

> Complète `CLAUDE.md` (upstream) avec les conventions internes au fork CETIC.

## Pourquoi ce fork

Upstream teutonet v1.5.0 hardcode les durations Certificate cert-manager à
`24h` (leaves) / `48h` (CAs) dans `pkg/hostedcontrolplane/controller.go`.
Sans intervention, **tout cluster HCP casse en split-brain TLS à
T+24h-T+48h** (bug upstream
[#87](https://github.com/teutonet/cluster-api-provider-hosted-control-plane/issues/87)).

Notre fork ajoute 2 env vars `CA_CERTIFICATE_DURATION` /
`CERTIFICATE_DURATION` (defaults inchangés = zero breaking change) qui
permettent l'override industry-standard (10y CAs, 1y leaves —
kubeadm/RKE2/EKS/GKE defaults). PR upstream proposée :
[teutonet#129](https://github.com/teutonet/cluster-api-provider-hosted-control-plane/pull/129).

Bonus dans le même commit : remplace `WithRenewBeforePercentage(50)` par
`WithRenewBefore(duration/2)` pour contourner un bug de validation
cert-manager qui rejette `renewBeforePercentage: 50` avec des durations
longues (87600h → "must result in a renewBefore greater than 5m0s" alors
que 50% de 87600h = 43800h ≫ 5m).

## Workflow de release CETIC

À chaque modif du code Go (rebase upstream OU patch interne) :

```bash
# 1. Build + push l'image avec tag CETIC incrémenté
docker build -f deploy/cetic/Dockerfile.cetic \
  -t registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.5.0-cetic.X .
docker push registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.5.0-cetic.X

# 2. Update le Deployment HCP controller mgmt-side
KUBECONFIG=~/.kube/k8s-capi-mgmt-prod.yaml \
  kubectl -n capi-hosted-control-plane-system \
  set image deploy/capi-hosted-control-plane-controller-manager \
  manager=registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v1.5.0-cetic.X

# 3. Wait Deployment Ready
KUBECONFIG=~/.kube/k8s-capi-mgmt-prod.yaml \
  kubectl -n capi-hosted-control-plane-system rollout status \
  deploy/capi-hosted-control-plane-controller-manager
```

Convention tag : `v<UPSTREAM>-cetic.<N>` (ex. `v1.5.0-cetic.2` =
2e build CETIC basé sur upstream v1.5.0).

## Rebase upstream

Quand teutonet sort une nouvelle release :

```bash
git fetch upstream                          # upstream = teutonet/...
git checkout main && git merge upstream/main
git checkout feat/configurable-cert-durations
git rebase main
# Résoudre conflits (probablement sur controller.go et operator.go)
# Re-test :
go build ./...
go test ./pkg/operator/... ./pkg/hostedcontrolplane/...
# Re-build image
docker build -f deploy/cetic/Dockerfile.cetic \
  -t registry.cloud.cetic-group.com/ccp/cluster-api-provider-hosted-control-plane:v<NEW>-cetic.1 .
```

## Branches

| Branch | Rôle |
|---|---|
| `main` | Mirror passive de `teutonet/main`, juste pour rebase |
| `feat/configurable-cert-durations` | **Branche active de notre fork** — contient les patch Go (commit 1) + les fichiers CETIC `deploy/cetic/` (commit 2). C'est ce que notre Deployment prod utilise. |
| `feat/configurable-cert-durations-upstream` | Branche dédiée à la **PR upstream** (juste le commit 1 du patch Go, pas les fichiers CETIC). Quand l'upstream rebase nécessaire, refaire cette branche : `git checkout -B feat/configurable-cert-durations-upstream <sha-du-commit-go>`. |

## Setup git remote

```bash
git remote add upstream git@github.com:teutonet/cluster-api-provider-hosted-control-plane.git
git remote -v
# origin    = cetic-group/cluster-api-provider-hosted-control-plane (notre fork)
# upstream  = teutonet/cluster-api-provider-hosted-control-plane    (officiel)
```

## Quand retirer le fork

Quand la PR upstream [teutonet#129](https://github.com/teutonet/cluster-api-provider-hosted-control-plane/pull/129)
sera mergée :

1. Update le Deployment HCP controller pour utiliser l'image officielle
   `ghcr.io/teutonet/cluster-api-provider-hosted-control-plane:vX.Y.Z`
2. Garder les env vars `CA_CERTIFICATE_DURATION` + `CERTIFICATE_DURATION`
   (lues par le binaire officiel grâce à notre PR)
3. Archiver notre fork (`gh repo archive cetic-group/cluster-api-provider-hosted-control-plane`)
4. Memory CCP `[[hcp-fork-2026-05-20]]` à update avec "retired YYYY-MM-DD"

## Note importante : NE PAS commit dans `main`

`main` doit rester une copie miroir de `upstream/main` pour faciliter les
rebases. Toutes nos modifs vivent sur `feat/configurable-cert-durations`
ou des branches dérivées.

Si on doit ajouter d'autres patches CETIC, créer une branche dédiée et la
merger dans `feat/configurable-cert-durations` (ou créer une nouvelle
branche `cetic-main` qui rebase régulièrement sur `feat/configurable-cert-durations`).
