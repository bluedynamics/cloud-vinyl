# Back spec.storage[].type=file with a volume

A `type: file` storage keeps cached objects in a file on disk instead of in the heap.
That file needs a volume.
Without one it would land in the container's writable layer, count against the node's image filesystem and can get unrelated pods evicted.

This guide shows three ways: (0) let the operator provision the volume, (1) an EmptyDir you declare yourself, or (2) a StorageClass-provisioned PVC per pod.

(file-storage-provisioned)=
## Option 0: let the operator provision the volume (simplest)

Since 1.1, leave out `path` and the operator does the wiring:

```yaml
spec:
  storage:
    - name: disk
      type: file
      size: 10Gi
```

For every `type: file` storage without `path` the operator

- adds an `emptyDir` volume with `sizeLimit` set to `size` plus 5 % (at least plus 64Mi).
  A full cache file then never trips the limit.
- mounts it at `/var/lib/vinyl-storage/<name>` and passes `/var/lib/vinyl-storage/<name>/<name>.bin` to varnishd.
- adds the sum of these volumes to the varnish container's `requests.ephemeral-storage`.
  The scheduler then only places the pod on a node with that much free disk.
  An `ephemeral-storage` request you set yourself in `spec.resources` takes precedence.

This form requires `size`.
The operator reserves `/var/lib/vinyl-storage`.
Neither `path` nor `spec.pod.volumeMounts` may point into it.

Use option 1 or 2 when you need a different volume type, a StorageClass, or persistence across restarts.

(ssd-backed-storage-emptydir)=

## Option 1: EmptyDir with `sizeLimit`

Useful when you want to cap cache disk use and isolate it from node ephemeral capacity, without per-pod persistence.

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
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

The file lives on node ephemeral storage, capped at 60 GiB regardless of how much the node has.

(ssd-backed-storage-pvc)=

## Option 2: PVC per pod via `volumeClaimTemplates`

Useful for persistence across pod restarts (faster warmup after a roll).
Also useful for isolating cache I/O onto a dedicated SSD StorageClass (`hcloud-volumes`, `gp3`, a CSI-driver-provisioned NVMe, etc.):

```yaml
apiVersion: vinyl.bluedynamics.eu/v1
kind: VinylCache
metadata:
  name: my-cache
spec:
  replicas: 2
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

The StatefulSet creates one PVC per replica, named after the pod ordinal: `cache-ssd-my-cache-0`, `cache-ssd-my-cache-1`.
They persist across pod deletion (StatefulSet semantics).
A rolling restart keeps the warmed cache.

Keep `spec.storage[].size` well under the PVC size.
The daemon needs filesystem overhead: allow about 20%.

### Changing the claim templates later

You cannot edit `spec.volumeClaimTemplates` once the VinylCache exists, as with a StatefulSet.
The admission webhook rejects the change.
The PVCs also outlive the VinylCache: a recreated cache binds the existing claims again, at their old size.
To grow the volumes, resize the PVCs directly if the StorageClass allows volume expansion.
Otherwise, delete the VinylCache and its PVCs and create it again.

(ssd-backed-storage-reserved)=

## Reserved names and paths

Do not use these names for your volumes.
They collide with operator-managed volumes:

- `agent-token`, `varnish-secret`, `varnish-workdir`, `varnish-tmp`, `bootstrap-vcl`

The operator reserves these mount paths:

- `/run/vinyl`, `/etc/varnish/secret`, `/etc/varnish/default.vcl`, `/var/lib/varnish`, `/tmp`, `/var/lib/vinyl-storage`

`spec.storage[].path` cannot live under a reserved mount.
Declare your own `pod.volumeMounts` entry that covers the path.

The admission webhook rejects violations with a clear message.
The webhook admits a `path` under none of your `pod.volumeMounts` with a warning.
The file would then land in the container's writable layer.

(ssd-backed-storage-memory)=

## Memory with a file storage

varnishd `mmap`s the cache file.
The objects it serves live in the kernel's page cache.
The page cache counts against the container's memory limit.
The kernel reclaims it under pressure instead of OOM-killing varnishd.
The memory limit only has to cover the process itself plus whatever share of hot objects should stay in RAM.
The process itself means object headers, threads, workspaces and the `Transient` storage.
With `malloc` storage, the limit must cover the storage itself plus heap fragmentation.
See [Size memory for malloc storage](size-memory.md).
