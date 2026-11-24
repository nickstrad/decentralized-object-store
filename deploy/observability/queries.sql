CREATE OR REPLACE VIEW snapshots_v1 AS
SELECT * FROM read_csv('*.csv', header=true, nullstr='', columns={
 'schema_version':'UINTEGER', 'node_id':'VARCHAR', 'region':'VARCHAR',
 'boot_id':'VARCHAR', 'sequence':'UBIGINT', 'start_unix_nano':'BIGINT',
 'time_unix_nano':'BIGINT', 'component':'VARCHAR', 'name':'VARCHAR',
 'kind':'VARCHAR', 'unit':'VARCHAR', 'labels_json':'VARCHAR',
 'counter_uint64':'UBIGINT', 'gauge_uint64':'UBIGINT'
});

CREATE OR REPLACE VIEW counter_deltas_v1 AS
WITH unique_samples AS (
 SELECT DISTINCT * FROM snapshots_v1 WHERE kind='counter'
), previous AS (
 SELECT *, lag(counter_uint64) OVER series AS previous_value,
 lag(time_unix_nano) OVER series AS previous_time
 FROM unique_samples
 WINDOW series AS (PARTITION BY node_id, boot_id, name, labels_json ORDER BY sequence)
)
SELECT *, CASE WHEN counter_uint64 >= previous_value
 THEN counter_uint64-previous_value ELSE NULL END AS delta,
 (time_unix_nano::HUGEINT-previous_time::HUGEINT)/1e9 AS elapsed_seconds
FROM previous;

CREATE OR REPLACE VIEW occ_discards_v1 AS
SELECT node_id, boot_id, sequence, time_unix_nano,
 counter_uint64, delta
FROM counter_deltas_v1 WHERE name='objstore_occ_lww_discards_total';

CREATE OR REPLACE VIEW http_rates_v1 AS
SELECT node_id, boot_id, sequence,
 json_extract_string(labels_json, '$.operation') AS operation,
 json_extract_string(labels_json, '$.status_class') AS status_class,
 delta/NULLIF(elapsed_seconds,0) AS requests_per_second
FROM counter_deltas_v1 WHERE name='objstore_http_requests_total';

CREATE OR REPLACE VIEW http_latency_v1 AS
WITH durations AS (
 SELECT node_id, boot_id, sequence,
 json_extract_string(labels_json, '$.operation') AS operation, delta AS duration_ns
 FROM counter_deltas_v1 WHERE name='objstore_http_request_duration_nanoseconds_total'
), requests AS (
 SELECT node_id, boot_id, sequence,
 json_extract_string(labels_json, '$.operation') AS operation, sum(delta) AS request_count
 FROM counter_deltas_v1 WHERE name='objstore_http_request_duration_count_total'
 GROUP BY ALL
)
SELECT d.*, duration_ns/NULLIF(request_count,0) AS mean_latency_ns
FROM durations d JOIN requests r USING(node_id,boot_id,sequence,operation);

CREATE OR REPLACE VIEW in_flight_v1 AS
SELECT node_id, boot_id, sequence, time_unix_nano, gauge_uint64 AS in_flight
FROM snapshots_v1 WHERE name='objstore_http_requests_in_flight';

SELECT * FROM occ_discards_v1 ORDER BY node_id,boot_id,sequence;
SELECT * FROM http_rates_v1 ORDER BY node_id,boot_id,sequence,operation,status_class;
SELECT * FROM http_latency_v1 ORDER BY node_id,boot_id,sequence,operation;
SELECT * FROM in_flight_v1 ORDER BY node_id,boot_id,sequence;
