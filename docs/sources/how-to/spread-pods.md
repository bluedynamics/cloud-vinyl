# Spread cache pods across nodes

With `spec.cluster.enabled`, each pod of a VinylCache holds its own shard of the cache.
If two pods share a node, losing that node empties both shards at once and sends their traffic to the backends together.
The operator gives every VinylCache a pod anti-affinity that keeps its pods apart.
This guide shows how to tune that rule for the whole cluster and for a single cache.

(spread-pods-default)=

## The default

Without any configuration, the operator adds a *preferred* anti-affinity on `kubernetes.io/hostname` with weight 100.
The scheduler spreads the pods over nodes when it can and still places them together when it has to.
On a small cluster with a node down, a degraded cache on the remaining node beats a `Pending` pod.

The rule selects the cache's own pods by the label `vinyl.bluedynamics.eu/cache-name`.
The operator sets it for every replica count.
Scaling from one replica to two does not restart the first pod.

A preferred rule only counts when the scheduler places a pod.
Pods that ended up on the same node stay there after the other node returns.
A descheduler with the `RemoveDuplicates` strategy moves them apart again.

(spread-pods-change-default)=

## Change the default for all caches

Set the chart values of the operator:

```yaml
defaults:
  podAntiAffinity:
    type: required                            # preferred | required | none
    topologyKey: topology.kubernetes.io/zone
    weight: 100                               # preferred only, 1-100
```

`required` leaves a pod `Pending` rather than placing it in a domain that already runs a pod of the same cache.
Use it only when the cluster has room for a rolling update.
It needs at least as many domains (nodes, zones) as the largest cache has replicas.

`none` turns the default off.

(spread-pods-override)=

## Override it for one cache

`spec.pod.podAntiAffinity` takes the same three fields.
Each field you set replaces the operator default for this cache.
The others keep it:

```yaml
spec:
  pod:
    podAntiAffinity:
      type: none
```

(spread-pods-own-rule)=

## Bring your own rule

When you set `spec.pod.affinity.podAntiAffinity`, the operator uses it as given and generates nothing.
The admission webhook rejects setting it together with `spec.pod.podAntiAffinity`.

`spec.pod.affinity.nodeAffinity` and `podAffinity` do not affect the default.
A cache pinned to a node pool with `nodeAffinity` is still spread inside that pool.

(spread-pods-roll-out)=

## Roll-out

Changing the rule changes the pod template.
The StatefulSet then restarts every cache pod, one at a time.
Each pod comes back with an empty cache.
