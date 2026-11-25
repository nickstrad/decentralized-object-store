# Object Store

A leaderless, multi-region object store written in Go. The platform (the node, the simulator and everything under `internal/`) uses the standard library alone, with `pgregory.net/rapid` restricted to test files; only the separate `objctl` client CLI (`cmd/objctl`) also uses `github.com/spf13/cobra`. A check enforces both rules. Every claim about durability, repair, and convergence is backed by deterministic simulation testing, property tests, and fuzzing. Metadata updates use optimistic concurrency control for conditional writes, while concurrent writes across partitions settle by last-writer-wins (highest hybrid logical clock, node ID breaking ties) so all replicas converge; every discarded write is counted and logged.

## Read the book

Read the book at [nickstrad.github.io/object-store](https://nickstrad.github.io/object-store/) or in this repository: each chapter explains one mechanism, maps it to packages and code excerpts, and states what the tests prove and do not.

- [Preface](book/front/preface.md) — scope, reading order, and stated limits
- [Topic Map](book/front/topic-map.md) — topics, packages, and main test targets

1. [Deterministic Simulation Testing](book/chapters/01-deterministic-simulation.md)
2. [SWIM Gossip Membership](book/chapters/02-swim-gossip.md)
3. [Reed-Solomon Erasure Coding](book/chapters/03-reed-solomon-erasure-coding.md)
4. [Merkle-Tree Anti-Entropy](book/chapters/04-merkle-anti-entropy.md)
5. [Optimistic Concurrency Control](book/chapters/05-optimistic-concurrency-control.md)
6. [Sagas](book/chapters/06-sagas.md)
7. [The Object Store and Sync Engine](book/chapters/07-object-store-and-sync-engine.md)
8. [End-to-End Tests and Hardening](book/chapters/08-end-to-end-and-hardening.md)

## What is in the repository

| Part | Scope |
| --- | --- |
| Environment and checks | Docker image with Go 1.26.11, `Makefile` targets for building, testing and running a cluster, automated tests, and documentation site build |
| Deterministic simulator | `internal/env`, event loop and nemesis (`internal/sim`, `simtest`), `cmd/objsim` |
| SWIM gossip membership | Direct/indirect probing, suspicion, refutation, tombstones, push-pull sync (`internal/gossip`) |
| Reed-Solomon erasure coding | GF(2^8) Cauchy codec (`internal/erasure`, `4+2` default), chunk store, striping, placement (`internal/storage`) |
| Merkle-tree anti-entropy | Fanout-16 SHA-256 trie, bounded diff protocol, scrubber and repair worker (`internal/merkle`) |
| OCC metadata | ETags, `If-Match`/`If-None-Match`, version vectors, last-writer-wins with logged discards, quotas (`internal/occ`) |
| Sagas | Checksummed log, crash recovery, five-step bucket migration (`internal/saga`, `sagasim`) |
| Node, runtime, and API | Wired node (`internal/node`), real runtime (`internal/noderuntime`), object API (`httpapi`), `cmd/objnode` |
| CLI client | `objctl` (`cmd/objctl`, `internal/objctl`) over a typed HTTP client (`internal/storage/httpclient`) |
| Load generator | `objload` (`cmd/objload`, `internal/objload`): a constant, seeded random request rate against the HTTP API, for watching metrics |
| Observability | Framework-free metric catalogue (`internal/metrics`), wired component/runtime/HTTP snapshots, off-by-default runner and `otel`/`prometheus`/`csv` adapters (`internal/telemetry`), `objnode` flags, pinned Docker Collector/Prometheus stack (`compose.observability.yaml`), and DuckDB-over-CSV queries (`deploy/observability/`) |
| End-to-end tests | Simulated clusters and real node processes, with network and storage failures injected during tests (`test/e2e`) |
| Book | Preface, topic map, eight chapters, C4 diagrams, published site (`book/`, `docs/`) |

## Development

You need Docker with Compose v2 on Linux or Docker Desktop on macOS, along with `make`, `git`, `curl`, and a POSIX shell. `make test-cluster`, `make test-cluster-csv` and the full gate also need `jq` on the host. Go runs inside the container, so the host needs no Go toolchain. The same commands use Docker's default platform, including ARM64 on Apple Silicon.

`make help` lists the Make targets.

Build the dev image (Go 1.26.11 on Debian bookworm). Every other target that needs the image builds it first, so you rarely run this by hand:

```sh
make dev
```

Open a shell in the dev container with `make shell`; the checkout is mounted at `/src` and Go modules and the build cache live in named volumes, so they survive between runs:

```text
make shell
```

Run a single `go` command the same way, without a shell:

```sh
docker compose run --rm dev go version
```

`compose.yaml` fixes the Compose project name (`objstore`), so a second checkout on the same machine shares containers with the first. If you keep two checkouts, set `COMPOSE_PROJECT_NAME` to a different value in one of them.

## Build

Build the four binaries, `objnode` (the storage node), `objsim` (the deterministic simulator), `objctl` (the CLI client) and `objload` (the load generator), into `bin/`, which git ignores:

```sh
make build
```

`make build` compiles inside the dev container, so the binaries are Linux binaries for Docker's default architecture and run in the dev image and the cluster containers, not directly on a macOS host. On Linux they are owned by root, because the container runs as root. If you have a local Go 1.26 toolchain, `go build ./cmd/...` or `go run ./cmd/objctl` on the host works too; the walkthrough below does not need it.

The platform packages import only the standard library (plus `pgregory.net/rapid` in tests), and only `cmd/objctl` may import `github.com/spf13/cobra`. The dependency check in `agent_work/gates/ci-fast.sh` and the full gate fails otherwise.

## Test

Every test runs in Docker, almost all of it in the dev container. The table lists each command; notes for some rows follow.

| Command | What it proves | Needs on the host | Time |
| --- | --- | --- | --- |
| `make test` | The whole Go test suite (`go test ./...`): unit, property, simulation and simulated end-to-end tests | Docker | about 7 min on 4 cores |
| `agent_work/gates/ci-fast.sh` | What CI runs on every push: formatting, build, vet, determinism and dependency lints, `go test -short` with coverage floors, and proof the run was in the dev container | Docker, git | about 3 min |
| `make test-cluster` | A three-node Compose cluster serves cross-node bucket and object reads and writes, and answers failed conditional writes with HTTP 412 | Docker, `curl`, `jq` | under 1 min* |
| `make test-cluster-csv` | The same as `make test-cluster`, with the `csv` metrics adapter on, keeping one metrics directory per node under `test-artifacts/` | Docker, `curl`, `jq` | under 1 min* |
| `make test-objctl` | The `objctl` binary against three real `objnode` processes: a scripted session, and a seeded random sequence of commands checked against a model | Docker | under 1 min* |
| `make test-csv` | A real `objnode` with the `csv` adapter writes metrics from all ten components that DuckDB reads back | Docker | under 1 min* |
| `make test-csv-fixture` | The DuckDB queries compute exact counters, rates and latencies over controlled CSV data and ignore unfinished files | Docker | under 1 min* |
| `make test-otel` | Real `objnode` metrics reach Prometheus through the OpenTelemetry Collector | Docker | under 1 min* |
| `make query-csv CSV_DIR=<dir>` | Not a test: runs the standard DuckDB report over a directory of CSV metrics, such as one a CSV target above printed | Docker | seconds |
| `make fuzz PKG=<pkg> FUZZ=<target>` | One native fuzz target for `FUZZTIME` (default `30s`) | Docker | `FUZZTIME` |
| `GATE_STAGE=final agent_work/gates/gate.sh demo` | The full landing gate (see below) | Docker, git, `jq`, `sha256sum` | about 2.5 h on 4 cores |

The times for `make test`, `ci-fast.sh` and the full gate are for a 4-core machine. The rows marked * were measured with warm Go caches on an 18-core machine. The first run of any target also builds the dev image and fills the Go module and build caches, which takes longer.

Run the whole suite, then the same quick checks as CI:

```sh
make test
agent_work/gates/ci-fast.sh
```

`ci-fast.sh` writes its result to `agent_work/ci/gates/result.json`; the result is marked partial, because only the full gate counts for landing a change.

**`make test-objctl`.** Set `OBJCTL_TRANSCRIPT` to keep a readable transcript of every `objctl` invocation the tests made, with its arguments, input, output and exit code. A relative path lands in the directory of the test package that ran, `test/e2e/`; `.gitignore` excludes `objctl-transcript*.txt`.

```text
OBJCTL_TRANSCRIPT=objctl-transcript.txt make test-objctl
```

**CSV and OTel targets.** `make test-csv`, `make test-csv-fixture` and `make test-cluster-csv` each create a fresh directory under `test-artifacts/` and print the matching `make query-csv` command (the cluster run prints one per node). `make test-csv` and `make test-csv-fixture` also print what the run proves, the report, and a VERDICT table with one row per check; any FAIL makes the target exit non-zero. `make test-otel` keeps its logs under `test-artifacts/otel/`. [docs/observability.md](docs/observability.md) explains the reports.

```text
make query-csv CSV_DIR=test-artifacts/node-csv.XXXXXXXX/metrics
```

**Fuzzing.** `make fuzz` runs one target; list the targets of a package with `go test -list`:

```text
docker compose run --rm dev go test -list '^Fuzz' ./internal/erasure
make fuzz PKG=./internal/erasure FUZZ=FuzzGF256 FUZZTIME=1m
```

To fuzz every target for a chosen budget, as the full gate does, run `docker compose run --rm -T -e FUZZ_SECONDS=300 dev agent_work/gates/fuzz-short.sh`.

**The full gate.** It runs end-to-end scenarios against real `objnode` processes, checks unit test coverage, runs property tests, fuzzes inputs, and checks for data races. It also sweeps 500 simulation seeds and starts a three-node cluster to verify cross-node reads and writes and HTTP 412 responses for failed preconditions. The `demo` argument names the output directory (`agent_work/demo/gates/`). `GATE_ONLY=G5` (for example) runs one gate of it; the header of `agent_work/gates/gate.sh` lists the options.

```text
GATE_STAGE=final agent_work/gates/gate.sh demo
```

### Replay a seed

Every failing single-seed simulator test prints a `REPLAY:` line with the exact test filter and seed so the failure reproduces byte for byte (aggregate sweep checks rerun the sweep). Replay a pinned single-seed nemesis run or the 100-node chaos CLI (same two SHA-256 digests per seed):

```sh
docker compose run --rm dev go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$/^shard=1$/^seed=1$' -seed=1 -count=1
docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3
```

## Run a cluster and use the CLI

`compose.yaml` defines a three-node `cluster` profile: `node-a1`, `node-b1` and `node-c1` in regions `a`, `b` and `c`, with `2+1` erasure coding. Each node runs `go run ./cmd/objnode` from the mounted checkout, serves HTTP on port 8080 inside the Compose network, and publishes it on an ephemeral `127.0.0.1` port on the host. `make cluster-up` starts the three nodes and waits until `node-a1` is ready and sees three live members:

```sh
make cluster-up
```

### With objctl

`objctl` addresses objects as `bucket/key`. The simplest way to run it on Linux and macOS alike is the Linux binary from `make build`, run inside the `node-a1` container, where the nodes are reachable by service name and the default address, `127.0.0.1:8080`, is `node-a1` itself. A shell function keeps the commands short; `-T` lets you pipe data into `put`, and `-e OBJCTL_ADDR` passes the host's `OBJCTL_ADDR`, if set, as the default `--addr`:

```sh
objctl() { docker compose --profile cluster exec -T -e OBJCTL_ADDR node-a1 bin/objctl "$@"; }
objctl health
objctl members
```

Create a bucket and write an object through `node-a1`, then read it back through `node-c1`. Object metadata reaches the other nodes by gossip within a few seconds, so a read from another node right after a write can briefly answer not found (exit 3):

```sh
objctl mb demo
echo "hello from region a" | objctl put --content-type text/plain demo/notes/hello.txt
until objctl --addr node-c1:8080 get demo/notes/hello.txt; do sleep 1; done
```

List, inspect and tag the object:

```sh
objctl ls demo
objctl head demo/notes/hello.txt
objctl tag set demo/notes/hello.txt owner alice
objctl tag ls demo/notes/hello.txt
```

Conditional writes use `--if-match <etag>` and `--if-none-match '*'`. The object exists, so a create-only put fails with exit 4 (HTTP 412):

```sh
objctl put --if-none-match '*' demo/notes/hello.txt < /dev/null || echo "exit $?"
```

`--json` prints one JSON document (the object store's own entity types), and `-v` (`--verbose`) traces each HTTP request and response on stderr. `OBJCTL_ADDR` sets the default address for every command:

```sh
OBJCTL_ADDR=node-c1:8080 objctl --json ls --prefix notes/ demo
objctl -v head demo/notes/hello.txt
```

Delete the object; reading it afterwards exits 3:

```sh
objctl rm demo/notes/hello.txt
objctl get demo/notes/hello.txt || echo "exit $?"
```

`objctl` also sets and shows bucket ACLs (`acl`), quotas (`quota`) and region migrations (`migrate`); `objctl --help` and `objctl <command> --help` list every command and flag. It exits with:

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | The request failed: server error, conflict, quota exceeded, or network |
| 2 | Invalid usage, or an input the client refuses before sending it |
| 3 | Not found: no such bucket, object, ACL entry or migration (HTTP 404) |
| 4 | Precondition failed: `--if-match` or `--if-none-match` did not hold (HTTP 412) |

With a local Go 1.26 toolchain you can also run the client on the host against a node's published port:

```text
go run ./cmd/objctl --addr "$(docker compose --profile cluster port node-a1 8080)" health
```

### With curl

The same API needs no client at all. Look up the published ports, PUT an object through `node-a1`, and GET it back from `node-c1`:

```sh
A=$(docker compose --profile cluster port node-a1 8080)
C=$(docker compose --profile cluster port node-c1 8080)
curl -sf "http://$A/_health"
echo
curl -sf -X PUT "http://$A/web"
echo
curl -sf -X PUT --data-binary "hello from region a" "http://$A/web/hello.txt"
echo
i=0; until curl -sf "http://$C/web/hello.txt"; do
  i=$((i+1)); [ "$i" -lt 60 ] || { docker compose --profile cluster logs --tail 20 node-c1; exit 1; }; sleep 1
done
echo
```

### Stop the cluster

Node data lives inside the containers, so stopping the cluster discards it:

```sh
make cluster-down
```

## Observability

Metrics are defined as plain standard-library value types in `internal/metrics` (83 catalogue descriptors across all 10 components) and collected on demand without framework lock-in. Telemetry is off by default and configured in `objnode`'s JSON config or via `-observability`, `-observability-adapter`, `-observability-interval`, `-observability-timeout`, and `-observability-settings`. Three stdlib-only adapters are built in: OTLP/HTTP JSON (`otel`, with a pinned Docker Compose Collector → Prometheus stack in `compose.observability.yaml`), direct Prometheus HTTP scraping (`prometheus`), and atomic local CSV segments (`csv`, paired with offline DuckDB SQL queries in `deploy/observability/queries.sql` and `query-csv.sh`). See [docs/observability.md](docs/observability.md) for the full catalogue, flag reference, adapter settings, DuckDB analysis setup, and adapter extension guide. The [Test](#test) section lists the observability checks.

### Watch metrics under load

`make metrics-up` builds the binaries and starts a stack you can look at in a browser: the three cluster nodes with the `otel` adapter on, the OpenTelemetry Collector, and Prometheus. It waits until Prometheus scrapes all three nodes, then prints the Prometheus UI address, its targets page, graph links for request rate, mean latency and requests in flight over the last five minutes, and the nodes' API addresses. Prometheus listens on `127.0.0.1:9090`; set `PROMETHEUS_PORT` to use another port. Nodes export every second (`METRICS_INTERVAL`, default `1s`) and Prometheus scrapes every second, so the graphs follow a change within a few seconds. The stack is its own Compose project, `objstore-metrics` (or `$COMPOSE_PROJECT_NAME-metrics`), so it leaves a `make cluster-up` cluster alone.

```text
make metrics-up
```

Then start the load in a second terminal. `make load` runs `objload` in the dev container on the stack's network. It creates two buckets if they are missing and sends 20 requests per second, round-robin over the three nodes, until you press Ctrl-C. The default `simple` profile is mostly GET and PUT with some HEAD, LIST and DELETE over 100 keys per bucket, with payloads of 1 KiB to 16 KiB:

```text
make load
make load RATE=100 PROFILE=mixed DURATION=2m
```

`objload` prints one line per second with the achieved rate, counts by status class and the mean and maximum latency, and a summary by operation when it stops. The rate is open loop: request *n* is due at *n*/rate seconds whatever the latency of earlier requests, so a slow cluster shows up as higher latency and, once every worker is busy, as requests reported `MISSED`, never as a quietly lower rate. 404 and 412 responses are expected outcomes of random requests, not failures; network errors and timeouts are counted and the run goes on, so you can stop and start a node under load. The start line prints the seed; `SEED=<n>` repeats the same request sequence. The variables `RATE`, `DURATION`, `PROFILE`, `WORKERS`, `KEYS`, `SIZE` (for example `512-64K`), `SEED` and `ADDR` map to `objload`'s flags; `docker compose run --rm dev bin/objload -h` describes them.

What to look at in the printed graphs:

- **Request rate**, `sum by (operation, status_class) (rate(objstore_http_requests_total[15s]))`: `object` requests in `2xx` and `4xx`, and `bucket` (listings) in `2xx`. The 404s are reads of keys not written yet, so they fall as the key space fills. Summed over all series, the rate matches the rate `objload` reports.
- **Mean latency**, the rate of `objstore_http_request_duration_nanoseconds_total` divided by the rate of `objstore_http_request_duration_count_total`, per operation, in milliseconds.
- **Requests in flight**, `objstore_http_requests_in_flight` per node. A gauge sampled once a second; at the default rate it is mostly zero, and it rises as `RATE` approaches what the workers can sustain.

`PROFILE=mixed` adds `tag` requests, `health` and `members` requests, create-only writes (`If-None-Match: *`) and writes with an `If-Match` tag that never matches, which get 412, and reads of keys that never exist, which get 404. The metrics have one `4xx` class; the summary of `objload` splits it by status code.

`make metrics-urls` prints the addresses again, `make metrics-logs` follows the containers' logs, and `make metrics-down` stops the stack and discards its data:

```text
make metrics-down
```

## Where things are

- **Book:** [book/manifest.json](book/manifest.json) (front matter in `book/front/`, chapters in `book/chapters/`).
- **C4 diagrams:** [docs/c4/README.md](docs/c4/README.md) (system, container, and component views).
- **Observability:** [docs/observability.md](docs/observability.md) (metric catalogue, `objnode` flags, `otel`/`prometheus`/`csv` adapters, DuckDB-over-CSV analysis, Collector → Prometheus setup, and adapter extension guide).
- **Make targets:** [Makefile](Makefile) (dev image, build, cluster, metrics stack, load generator, and every test target); `make help` lists them.
- **Gate scripts:** `agent_work/gates/` (`ci-fast.sh`, `gate.sh`, `fuzz-short.sh`, and `readme-commands.sh`, which runs every `sh` block of this README on a clean clone).
