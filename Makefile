OBS_ANALYSIS_IMAGE ?= objstore-dev-observability:local

export OBS_ANALYSIS_IMAGE CSV_DIR

.PHONY: dev test observability query-csv

dev:
	docker compose build dev

test: dev
	docker compose run --rm dev go test ./...

observability:
	docker build --target dev-observability -t "$$OBS_ANALYSIS_IMAGE" .

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
