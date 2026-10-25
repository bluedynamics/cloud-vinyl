# Size memory for malloc storage

A `malloc` storage lives in the heap of the varnishd child process. The
container memory limit therefore has to cover more than `spec.storage[].size`.
This guide explains what else needs room, how the operator tunes the heap, and
which counters show that a cache is too small.

## Budget the memory limit

Plan the container memory limit as the sum of:

- the `malloc` storage sizes from `spec.storage` (100 MB for the default
  storage when `spec.storage` is empty),
- the `Transient` storage, which holds uncacheable and short-lived objects
  and is unbounded by default,
- per-thread workspaces (`thread_pool_max` threads, each with its own
  workspaces),
- heap overhead: memory that was freed but not yet returned to the kernel,
  and fragmentation.

The last item is the one that surprises. Under eviction churn (objects
constantly nuked to make room for new ones) the heap overhead can exceed the
storage size itself. A cache with `malloc,1G` has been observed at 2.5 GiB RSS
with jemalloc's default settings. With the operator's tuning described below
it stays much closer to the storage size, but leave a generous margin:

```yaml
spec:
  storage:
    - name: cache
      type: malloc
      size: 1Gi
  resources:
    requests:
      memory: 2Gi
    limits:
      cpu: "1"
      memory: 2Gi
```

When a pod is OOMKilled, compare `varnish_sma_g_bytes` of the storage with the
container's working set. A storage that sits at its size while the working set
keeps growing points at heap overhead, not at the storage.

## How the operator tunes jemalloc

varnishd in the stock image allocates through jemalloc. The operator sets
`MALLOC_CONF` on the varnish container to

```
thp:never,background_thread:true,dirty_decay_ms:5000,muzzy_decay_ms:0,narenas:<4 × CPU limit>
```

- `thp:never` keeps transparent huge pages away from the heap. On hosts with
  THP set to `always`, sparsely used extents are otherwise backed by 2 MiB
  pages. The setting uses `MADV_NOHUGEPAGE`, so it works whatever the host
  setting is.
- `background_thread:true` returns freed pages to the kernel asynchronously,
  instead of only when an arena happens to see allocation activity.
- `dirty_decay_ms:5000` and `muzzy_decay_ms:0` give freed pages back after
  5 seconds and skip the lazily reclaimed "muzzy" state.
- `narenas` follows the CPU limit, rounded up to whole CPUs (0.5 CPU gives 4
  arenas). jemalloc itself counts the node's CPUs, not the container limit, so
  a pod limited to 0.5 CPU on a 20-core node would otherwise get 80 arenas.
  Without a CPU limit the operator leaves `narenas` at jemalloc's default.

If the image uses a different allocator, `MALLOC_CONF` has no effect.

## Override or disable the tuning

Set `MALLOC_CONF` in `spec.pod.env`. Your value replaces the operator's value
as a whole; it is not merged with it. An empty value restores jemalloc's own
defaults:

```yaml
spec:
  pod:
    env:
      - name: MALLOC_CONF
        value: "thp:never,background_thread:true,narenas:2"
```

jemalloc ignores options it does not know and prints
`<jemalloc>: Invalid conf pair` to the container log at startup. Check the log
after changing the value.

Changing `spec.pod.env` or the CPU limit changes the pod template, so the
StatefulSet rolls its pods. Upgrading to the operator version that introduced
this default rolls all pods once as well.

## Fetch failures with "Could not get storage"

When a new object does not fit, varnishd evicts up to `nuke_limit` objects
(default 50) from the least recently used end of the storage. If that is still
not enough room, the fetch fails, the log shows `Could not get storage`, and
the client gets a 503. This happens with large objects in a small storage.

Watch `varnish_sma_c_fail` per storage and `varnish_main_n_lru_nuked`. If
failures are frequent, either make the storage larger or raise `nuke_limit`
through `spec.varnishParameters`:

```yaml
spec:
  varnishParameters:
    nuke_limit: "500"
```

A higher limit lets one fetch evict more objects, which costs time on that
fetch. The right value depends on the object size distribution, so the
operator keeps varnishd's default.
