# cloud-vinyl

Kubernetes operator for managing [Vinyl Cache](https://vinyl-cache.org/) clusters
(the FOSS HTTP cache formerly known as Varnish Cache).

## Features

- **Operator pattern** — central controller with reconcile loop, no sidecar antipattern
- **Multiple backends** — first-class support for multiple backend services
- **VCL lifecycle management** — generates and pushes VCL config to Vinyl Cache pods via vinyl-agent
- **Invalidation** — integrated PURGE/BAN and xkey (surrogate key) support
- **Clustering** — shard director for Vinyl Cache peer routing
- **PROXY Protocol** — optional upstream PROXY protocol support
- **Non-root by default** — all containers run as non-root
- **Helm chart** — first-class Helm deployment

## Documentation

Rendered documentation: **https://bluedynamics.github.io/cloud-vinyl/**

## Installation

```sh
helm install cloud-vinyl oci://ghcr.io/bluedynamics/charts/cloud-vinyl
```

## Source Code and Contributions

The source code is managed in a Git repository, with its main branches hosted on GitHub. Issues can be reported there too.

We'd be happy to see many forks and pull requests to make this package even better. We welcome AI-assisted contributions, but expect every contributor to fully understand and be able to explain the code they submit. Please don't send bulk auto-generated pull requests.

Maintainers are Jens Klein and the BlueDynamics Alliance developer team. We appreciate any contribution and if support, coaching, integration or adaptations are needed, we also offer commercial support.

## License

Copyright 2026 BlueDynamics Alliance.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
