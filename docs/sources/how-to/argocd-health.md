(argocd-health)=
# Show VinylCache health in Argo CD

Argo CD knows nothing about the VinylCache resource and shows it without health.
A health check written in Lua maps the VinylCache status to Argo CD's health states.
Argo CD reads such checks only from its `argocd-cm` ConfigMap, under the key `resource.customizations.health.vinyl.bluedynamics.eu_VinylCache`.

```{note}
The `argocd.resourceCustomizations.enabled` value of the cloud-vinyl chart rendered the check into a ConfigMap of its own.
Argo CD never reads that ConfigMap, so the check never took effect (#189).
The value is deprecated and has no effect.
```

(argocd-health-script)=

## The health check

```{literalinclude} ../../../config/argocd/vinylcache-health.lua
:language: lua
```

The check reads `status.phase` first:

| Phase | Argo CD health | Message |
|---|---|---|
| `Ready` | Healthy | `VinylCache is ready` |
| `Pending` | Progressing | the message of the `Ready` condition |
| `Degraded`, `Error` | Degraded | `status.message`, else the message of a failing `BackendsAvailable` condition, else the phase |
| `Stopped` | Suspended | `scaled to zero replicas` |

Without a phase, the `Ready` condition decides.
Without a status, the check reports Progressing.
A cache switched off on purpose shows as Suspended, not as unhealthy, see {ref}`switch-off-a-cache`.

(argocd-health-helm)=

## Add it with the Argo CD Helm chart

If the `argo-cd` Helm chart manages Argo CD, add the check under `configs.cm`:

```yaml
configs:
  cm:
    resource.customizations.health.vinyl.bluedynamics.eu_VinylCache: |
      hs = {}
      -- the rest of the script above
```

(argocd-health-kubectl)=

## Add it with `kubectl`

Without the Helm chart, patch `argocd-cm` directly.
Save the script as `vinylcache-health.lua` and run:

```shell
kubectl -n argocd patch configmap argocd-cm --type merge --patch "$(
  jq -n --rawfile lua vinylcache-health.lua \
    '{data: {"resource.customizations.health.vinyl.bluedynamics.eu_VinylCache": $lua}}'
)"
```

A tool that manages `argocd-cm` from a repository overwrites a manual patch on its next sync.
Add the key there instead.

(argocd-health-check)=

## Check it

Argo CD picks up the change without a restart.
The VinylCache in the Application's resource tree then shows a health state and the message.
