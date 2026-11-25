OBS_ANALYSIS_IMAGE ?= objstore-dev-observability:local
FUZZTIME ?= 30s
CLUSTER_NODES = node-a1 node-b1 node-c1

export OBS_ANALYSIS_IMAGE CSV_DIR

.PHONY: dev shell build cluster-up cluster-down fuzz test observability query-csv test-csv test-csv-fixture test-otel test-cluster test-cluster-csv test-objctl

dev:
	docker compose build dev

shell: dev
	docker compose run --rm dev bash

build: dev
	docker compose run --rm -T dev go build -o bin/ ./cmd/objnode ./cmd/objsim ./cmd/objctl

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
