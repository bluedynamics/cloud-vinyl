# Back spec.storage[].type=file with a user-provisioned volume

By default, `spec.storage[].type=file` lands the cache spill file in the operator's `/var/lib/varnish` EmptyDir. That works for small working sets on nodes with SSD-backed ephemeral storage, but has limits: no persistence across pod restart, no `sizeLimit`, no choice of StorageClass, no isolation from node ephemeral use (logs, image layers).

This guide shows how to back the spill file with (1) an EmptyDir with an explicit size limit, or (2) a StorageClass-provisioned PVC per pod.

## Option 1 — EmptyDir with sizeLimit (simplest)

Useful when you want to cap cache disk use and isolate it from node ephemeral capacity, without per-pod persistence.

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
  image: varnish:7.6
  backends:
    - name: app
      serviceRef:
        name: app-service
      port: 8080
  storage:
    - name: mem
      type: malloc
      size: 1Gi
    - name: disk
      type: file
      path: /var/lib/varnish-cache/spill.bin
      size: 50Gi
  pod:
    volumes:
      - name: cache-scratch
        emptyDir:
          sizeLimit: 60Gi
    volumeMounts:
      - name: cache-scratch
        mountPath: /var/lib/varnish-cache
```

The file lives on node ephemeral storage, but capped at 60 GiB regardless of how much the node has.

## Option 2 — PVC per pod via `volumeClaimTemplates`

Useful for persistence across pod restarts (faster warmup after a roll), or for isolating cache I/O onto a dedicated SSD StorageClass (`hcloud-volumes`, `gp3`, a CSI-driver-provisioned NVMe, etc.):

```yaml
apiVersion: vinyl.bluedynamics.eu/v1alpha1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
  image: varnish:7.6
  backends:
    - name: app
      serviceRef:
        name: app-service
      port: 8080
  storage:
    - name: mem
      type: malloc
      size: 1Gi
    - name: disk
      type: file
      path: /var/lib/varnish-cache/spill.bin
      size: 80Gi
  volumeClaimTemplates:
    - metadata:
        name: cache-ssd
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: hcloud-volumes
        resources:
          requests:
            storage: 100Gi
  pod:
    volumeMounts:
      - name: cache-ssd
        mountPath: /var/lib/varnish-cache
```

The StatefulSet creates one PVC per replica — `cache-ssd-my-cache-0`, `cache-ssd-my-cache-1`, etc. They persist across pod deletion (StatefulSet semantics) so a rolling restart doesn't throw away the warmed cache.

Make sure `spec.storage[].size` is well under the PVC size (Varnish needs filesystem overhead — allow ~20%).

## Reserved names and paths

These names cannot be used for your volumes (they collide with operator-managed volumes):

- `agent-token`, `varnish-secret`, `varnish-workdir`, `varnish-tmp`, `bootstrap-vcl`

These mount paths are reserved by the operator:

- `/run/vinyl`, `/etc/varnish/secret`, `/etc/varnish/default.vcl`, `/var/lib/varnish`, `/tmp`

`spec.storage[].path` cannot live under a reserved mount — you must declare your own `pod.volumeMounts` entry that covers the path.

The admission webhook rejects violations with a clear message.
