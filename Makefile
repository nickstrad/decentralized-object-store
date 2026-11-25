OBS_ANALYSIS_IMAGE ?= objstore-dev-observability:local
FUZZTIME ?= 30s
CLUSTER_NODES = node-a1 node-b1 node-c1

export OBS_ANALYSIS_IMAGE CSV_DIR

.PHONY: dev shell build cluster-up cluster-down fuzz test observability query-csv test-csv test-csv-fixture test-otel test-cluster test-cluster-csv test-objctl
.PHONY: help metrics-up metrics-urls metrics-logs metrics-down load

dev:
	docker compose build dev

shell: dev
	docker compose run --rm dev bash

build: dev
	docker compose run --rm -T dev go build -o bin/ ./cmd/objnode ./cmd/objsim ./cmd/objctl ./cmd/objload

cluster-up: dev
	docker compose --profile cluster up -d $(CLUSTER_NODES)
	@i=0; until docker compose --profile cluster exec -T node-a1 curl -sf http://node-a1:8080/_health 2>/dev/null | grep -q '"Ready":true,"LiveMembers":3'; do \
		i=$$((i+1)); if [ $$i -ge 300 ]; then docker compose --profile cluster logs --tail 20 $(CLUSTER_NODES); exit 1; fi; sleep 1; \
	done; echo 'cluster ready: node-a1 sees three live members'

cluster-down:
	docker compose --profile cluster down

fuzz: dev
	@if [ -z "$(PKG)" ] || [ -z "$(FUZZ)" ]; then \
		echo 'Usage: make fuzz PKG=./internal/erasure FUZZ=FuzzGF256 [FUZZTIME=30s]' >&2; \
		exit 2; \
	fi
	docker compose run --rm -T dev go test $(PKG) -run '^$$' -fuzz '^$(FUZZ)$$' -fuzztime $(FUZZTIME) -fuzzminimizetime 5s

test: dev
	docker compose run --rm dev go test ./...

observability:
	docker build --target dev-observability -t "$$OBS_ANALYSIS_IMAGE" .

test-csv: observability
	test/observability/check-objnode-csv.sh

test-csv-fixture: observability
	test/observability/check-csv.sh

test-otel:
	test/observability/check-otel.sh

test-cluster: dev
	test/e2e/cluster-smoke.sh

test-cluster-csv: dev
	test/e2e/cluster-smoke.sh --csv

test-objctl: dev
	docker compose run --rm -e E2E_TIER_R=1 -e OBJCTL_TRANSCRIPT dev go test ./test/e2e -run 'Objctl' -count=1 -v

query-csv:
	@if [ -z "$$CSV_DIR" ] || [ ! -d "$$CSV_DIR" ]; then \
		echo 'Usage: make query-csv CSV_DIR=/path/to/metrics' >&2; \
		exit 2; \
	fi
	$(MAKE) observability
	@csv_dir=$$(cd -- "$$CSV_DIR" && pwd -P) && \
		docker run --rm --network none \
			-v "$$PWD:/src:ro" \
			-v "$$csv_dir:/metrics:ro" \
			"$$OBS_ANALYSIS_IMAGE" /src/deploy/observability/query-csv.sh /metrics

# Interactive metrics stack and load generator (deploy/observability/metrics-stack.sh): three
# nodes exporting through the Collector to Prometheus on 127.0.0.1:$(PROMETHEUS_PORT), and
# objload in the dev container on the stack's network. Variables mirror objload's flags;
# empty ones keep objload's defaults.
PROMETHEUS_PORT ?= 9090
METRICS_INTERVAL ?= 1s
ADDR ?= node-a1:8080,node-b1:8080,node-c1:8080
RATE ?= 20
PROFILE ?= simple
DURATION ?=
WORKERS ?=
KEYS ?=
SIZE ?=
SEED ?=
METRICS_STACK = PROMETHEUS_PORT=$(PROMETHEUS_PORT) METRICS_INTERVAL=$(METRICS_INTERVAL) deploy/observability/metrics-stack.sh

metrics-up: build
	$(METRICS_STACK) up

metrics-urls:
	@$(METRICS_STACK) urls

metrics-logs:
	$(METRICS_STACK) logs

metrics-down:
	$(METRICS_STACK) down

load: build
	$(METRICS_STACK) load -addr '$(ADDR)' -rate '$(RATE)' -profile '$(PROFILE)' \
		$(if $(DURATION),-duration '$(DURATION)') $(if $(WORKERS),-workers '$(WORKERS)') \
		$(if $(KEYS),-keys '$(KEYS)') $(if $(SIZE),-size '$(SIZE)') $(if $(SEED),-seed '$(SEED)')

help:
	@echo 'Build and run (README.md has the details):'
	@echo '  dev                  build the dev image (Go toolchain); most targets do this first'
	@echo '  shell                open a shell in the dev container'
	@echo '  build                build objnode, objsim, objctl and objload into bin/'
	@echo '  cluster-up           start the three-node cluster and wait until it is ready'
	@echo '  cluster-down         stop the cluster'
	@echo '  observability        build the DuckDB analysis image'
	@echo '  query-csv            DuckDB report over CSV metrics (CSV_DIR=<dir>)'
	@echo '  metrics-up           three nodes, Collector and Prometheus; prints the Prometheus URLs'
	@echo '  metrics-urls         print the Prometheus URLs and node addresses again'
	@echo '  metrics-logs         follow the metrics stack logs'
	@echo '  metrics-down         stop the metrics stack'
	@echo '  load                 constant random load on the metrics stack until Ctrl-C'
	@echo '                       (RATE=20 PROFILE=simple|mixed DURATION= WORKERS= KEYS= SIZE= SEED= ADDR=)'
	@echo 'Tests:'
	@echo '  test                 the whole Go test suite'
	@echo '  test-cluster         three-node Compose cluster smoke test (test-cluster-csv: with CSV metrics)'
	@echo '  test-objctl          objctl against three real nodes'
	@echo '  test-csv             objnode CSV metrics read back by DuckDB (test-csv-fixture: controlled data)'
	@echo '  test-otel            objnode metrics reach Prometheus through the Collector'
	@echo '  fuzz                 one fuzz target (PKG=<pkg> FUZZ=<target> [FUZZTIME=30s])'
