# Glossary

This page defines the abbreviations and terms the cloud-vinyl documentation uses.

```{glossary}
BAN
  A request that invalidates every cached object matching an expression.
  See {doc}`/explanation/invalidation`.

ban lurker
  A background thread of the cache daemon.
  It tests bans on `obj.http.*` against stored objects and drops a ban once no object can match it.

CSI
  Container Storage Interface.
  The Kubernetes plugin interface for storage drivers.

director
  A VCL object that groups backends and picks one per request, for example by consistent hashing (`shard`) or in turn (`round_robin`).
  cloud-vinyl generates one director per entry in `spec.backends`.

DNS
  Domain Name System.

ESI
  Edge Side Includes.
  Markup in a response that lets the cache assemble a page from fragments it caches separately.

flavor
  The lineage of the cache daemon, `vinyl` or `varnish`, selected with `spec.flavor`.
  See {doc}`/explanation/lineages`.

FOSS
  Free and open-source software.

FQDN
  Fully qualified domain name, such as `my-cache-invalidation.app.svc.cluster.local`.

GID
  Group ID of a process.

HTTP
  Hypertext Transfer Protocol.

IRSA
  IAM Roles for Service Accounts.
  The Amazon EKS mechanism that maps a Kubernetes service account to an IAM role.

OOM
  Out of memory.
  Kubernetes ends a container that exceeds its memory limit and reports it as `OOMKilled`.

PROXY protocol
  A protocol that passes the original client address through a proxy to the next hop.
  The cache accepts it only on a listener enabled with `spec.proxyProtocol.enabled`.

PURGE
  A request that removes the cached object of one URL and site host.
  See {doc}`/explanation/invalidation`.

RBAC
  Role-based access control, the Kubernetes authorization model of roles and bindings.

RSS
  Resident set size, the memory of a process that is held in RAM.

runtime catalog
  The list of flavors and versions in the chart values, each with a daemon, an exporter and a tracer image.
  See {ref}`helm-values-runtimes`.

SHA
  Secure Hash Algorithm.
  cloud-vinyl identifies VCL and pins images with SHA-256 digests.

SNAT
  Source network address translation.
  It replaces the source IP of a connection, for example at a node or a load balancer.

soft purge
  A purge that marks an object as stale instead of removing it.
  The cache keeps serving it while it fetches a fresh copy in the background.

SSD
  Solid-state drive.

THP
  Transparent huge pages.
  A Linux kernel feature that backs memory with 2 MiB pages.

TLS
  Transport Layer Security.

UID
  User ID of a process.
  The cache pods run as UID 65532.

URL
  Uniform Resource Locator.

VCL
  The configuration language of the cache daemon.
  The daemon compiles VCL at runtime.
  See {doc}`/explanation/vcl-lifecycle`.

VMOD
  A module that extends VCL, such as `vmod_xkey` or the directors module.

VSM
  The shared memory segment in which the cache daemon publishes its counters and log.
  `varnishstat` and the log tools read it.

xkey
  Surrogate keys, also called cache tags.
  Objects carry keys from an `Xkey` response header, and one request invalidates all objects with a key.
```
