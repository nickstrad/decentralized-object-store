-- Readable report over CSV metric segments (default SQL file of query-csv.sh).
--
-- query-csv.sh runs views.sql (the *_v1 views) before this file, from inside the
-- metrics directory. This file only adds presentation on top of those views:
-- report_* macros and views, and CLI narration (.print). Every number in the
-- narration is computed from the data present; nothing about the run is hard-coded.
--
-- Output is '.mode list' with headers off: a query that returns no rows prints
-- nothing, so a section prints its legend and table only when it has data, and prints
-- a sentence saying so when it does not.

.pager off
.mode list
.headers off

-- ===================================================================================
-- Presentation helpers
-- ===================================================================================

-- report_table(view_name): render a view as an aligned text table, or nothing if the
-- view is empty. The view must have a sort_key column (used for ordering, not shown).
-- Column names become the header, NULL shows as '-', and columns whose values all look
-- numeric (optionally with a time unit) are right-aligned.
CREATE OR REPLACE MACRO report_table(view_name) AS TABLE
WITH body AS (
    FROM query_table(view_name)
), cells AS (
    (SELECT 0 AS part, NULL AS sort_key, alias(COLUMNS(* EXCLUDE sort_key)) FROM body LIMIT 1)
    UNION ALL
    (SELECT 1, NULL, left(alias(COLUMNS(* EXCLUDE sort_key)), 0) FROM body LIMIT 1)
    UNION ALL
    SELECT 2, sort_key, coalesce(COLUMNS(* EXCLUDE sort_key)::VARCHAR, '-') FROM body
), padded AS (
    SELECT part, sort_key,
        CASE
            WHEN part = 1
                THEN repeat('-', max(length(COLUMNS(* EXCLUDE (part, sort_key)))) OVER ())
            WHEN bool_and(regexp_full_match(COLUMNS(* EXCLUDE (part, sort_key)),
                    '-|[-+]?[0-9][0-9.,]*( (ns|µs|ms|s))?')) FILTER (WHERE part = 2) OVER ()
                THEN lpad(COLUMNS(* EXCLUDE (part, sort_key)),
                    max(length(COLUMNS(* EXCLUDE (part, sort_key)))) OVER ()::INTEGER, ' ')
            ELSE rpad(COLUMNS(* EXCLUDE (part, sort_key)),
                    max(length(COLUMNS(* EXCLUDE (part, sort_key)))) OVER ()::INTEGER, ' ')
        END
    FROM cells
)
SELECT rtrim('  ' || concat_ws('  ', *COLUMNS(* EXCLUDE (part, sort_key))))
FROM padded
ORDER BY part, sort_key;

-- report_legend(view_name, title, entries): print a blank line, a title, one line per
-- column (entries is a list of [column, meaning] pairs) and a blank line, but only if
-- the view has rows, so a legend never describes a table that is not printed.
CREATE OR REPLACE MACRO report_legend(view_name, title, entries) AS TABLE
SELECT line
FROM (
    SELECT -1 AS i, '' AS line
    UNION ALL
    SELECT 0, title
    UNION ALL
    SELECT i, '  ' || rpad(entries[i][1],
            list_max([length(e[1]) FOR e IN entries])::INTEGER, ' ') || '  ' || entries[i][2]
    FROM generate_series(1, len(entries)) AS g(i)
    UNION ALL
    SELECT len(entries) + 1, ''
)
WHERE EXISTS (FROM query_table(view_name))
ORDER BY i;

-- Value formatting.
CREATE OR REPLACE MACRO report_boot(boot_id) AS left(boot_id, 8);
CREATE OR REPLACE MACRO report_time(unix_nano) AS
    strftime(make_timestamp(unix_nano // 1000), '%H:%M:%S.%g');
CREATE OR REPLACE MACRO report_datetime(unix_nano) AS
    strftime(make_timestamp(unix_nano // 1000), '%Y-%m-%d %H:%M:%S.%g');
CREATE OR REPLACE MACRO report_duration(ns) AS
    CASE
        WHEN ns IS NULL THEN NULL
        WHEN ns < 1e3 THEN printf('%d ns', round(ns)::BIGINT)
        WHEN ns < 1e6 THEN printf('%.1f µs', ns / 1e3)
        WHEN ns < 1e9 THEN printf('%.2f ms', ns / 1e6)
        ELSE printf('%.3f s', ns / 1e9)
    END;
CREATE OR REPLACE MACRO report_rate(per_second) AS printf('%.1f', per_second);
-- report_count(n, singular, plural): '1 snapshot', '3 snapshots'.
CREATE OR REPLACE MACRO report_count(n, singular, plural) AS
    printf('%d %s', n, CASE WHEN n = 1 THEN singular ELSE plural END);

-- ===================================================================================
-- Report views (each *_table view feeds report_table)
-- ===================================================================================

-- report_snapshots: one row per snapshot, with the gap to the previous snapshot of the
-- same boot.
CREATE OR REPLACE VIEW report_snapshots AS
WITH batches AS (
    SELECT DISTINCT node_id, region, boot_id, sequence, start_unix_nano, time_unix_nano
    FROM snapshots_v1
)
SELECT *,
    time_unix_nano - lag(time_unix_nano) OVER (
        PARTITION BY node_id, boot_id ORDER BY sequence) AS gap_ns
FROM batches;

-- report_boots: one row per process lifetime (node_id, boot_id).
CREATE OR REPLACE VIEW report_boots AS
SELECT node_id, region, boot_id,
    count(*) AS snapshots,
    min(sequence) AS first_sequence,
    max(sequence) AS last_sequence,
    min(start_unix_nano) AS start_unix_nano,
    min(time_unix_nano) AS first_time,
    max(time_unix_nano) AS last_time,
    median(gap_ns) AS median_gap_ns
FROM report_snapshots
GROUP BY ALL;

CREATE OR REPLACE VIEW report_boots_table AS
SELECT
    row_number() OVER (ORDER BY first_time, node_id, boot_id) AS sort_key,
    node_id AS "node",
    region AS "region",
    report_boot(boot_id) AS "boot",
    snapshots AS "snapshots",
    printf('%d-%d', first_sequence, last_sequence) AS "sequences",
    report_datetime(start_unix_nano) AS "process start (UTC)",
    report_time(first_time) AS "first snapshot",
    printf('%.3f', (last_time - first_time) / 1e9) AS "span (s)",
    printf('%d', round(median_gap_ns / 1e6)::BIGINT) AS "interval (ms)"
FROM report_boots;

-- report_occ_table: snapshots whose interval added OCC discards, plus the first
-- snapshot of a boot if discards happened before it.
CREATE OR REPLACE VIEW report_occ_table AS
SELECT
    row_number() OVER (ORDER BY node_id, boot_id, sequence) AS sort_key,
    node_id AS "node",
    report_boot(boot_id) AS "boot",
    sequence AS "seq",
    report_time(time_unix_nano) AS "at (UTC)",
    delta AS "discards",
    counter_uint64 AS "since boot"
FROM occ_discards_v1
WHERE delta > 0 OR (delta IS NULL AND counter_uint64 > 0);

-- report_http_requests: objstore_http_requests_total per series and interval, with
-- the same rate arithmetic as http_rates_v1.
CREATE OR REPLACE VIEW report_http_requests AS
SELECT node_id, boot_id, sequence, time_unix_nano,
    json_extract_string(labels_json, '$.operation') AS operation,
    json_extract_string(labels_json, '$.status_class') AS status_class,
    counter_uint64 AS since_boot,
    delta AS requests,
    elapsed_seconds,
    delta / NULLIF(elapsed_seconds, 0) AS requests_per_second
FROM counter_deltas_v1
WHERE name = 'objstore_http_requests_total';

-- report_http_summary_table: one row per series that counted any request.
CREATE OR REPLACE VIEW report_http_summary_table AS
SELECT
    row_number() OVER (ORDER BY node_id, boot_id, operation, status_class) AS sort_key,
    node_id AS "node",
    report_boot(boot_id) AS "boot",
    operation AS "operation",
    status_class AS "status",
    max_by(since_boot, sequence) AS "since boot",
    coalesce(sum(requests), 0) AS "in window",
    count(*) FILTER (WHERE requests > 0) AS "active intervals",
    report_rate(max(requests_per_second) FILTER (WHERE requests > 0)) AS "peak req/s"
FROM report_http_requests
GROUP BY node_id, boot_id, operation, status_class
HAVING max(since_boot) > 0;

-- report_http_detail_table: one row per series and interval with requests.
CREATE OR REPLACE VIEW report_http_detail_table AS
SELECT
    row_number() OVER (ORDER BY node_id, boot_id, sequence, operation, status_class) AS sort_key,
    node_id AS "node",
    report_boot(boot_id) AS "boot",
    sequence AS "seq",
    report_time(time_unix_nano) AS "at (UTC)",
    operation AS "operation",
    status_class AS "status",
    requests AS "requests",
    printf('%d', round(elapsed_seconds * 1000)::BIGINT) AS "interval (ms)",
    report_rate(requests_per_second) AS "req/s"
FROM report_http_requests
WHERE requests > 0;

-- report_http_latency: http_latency_v1 with the per-interval request count and time.
CREATE OR REPLACE VIEW report_http_latency AS
SELECT l.node_id, l.boot_id, l.sequence, s.time_unix_nano, l.operation,
    l.duration_ns, c.delta AS requests, l.mean_latency_ns
FROM http_latency_v1 AS l
JOIN counter_deltas_v1 AS c
    ON c.name = 'objstore_http_request_duration_count_total'
    AND c.node_id = l.node_id AND c.boot_id = l.boot_id AND c.sequence = l.sequence
    AND json_extract_string(c.labels_json, '$.operation') = l.operation
JOIN report_snapshots AS s
    ON s.node_id = l.node_id AND s.boot_id = l.boot_id AND s.sequence = l.sequence;

-- report_latency_summary_table: one row per operation that timed any request.
CREATE OR REPLACE VIEW report_latency_summary_table AS
SELECT
    row_number() OVER (ORDER BY node_id, boot_id, operation) AS sort_key,
    node_id AS "node",
    report_boot(boot_id) AS "boot",
    operation AS "operation",
    sum(requests) AS "requests",
    count(*) AS "active intervals",
    report_duration(sum(duration_ns) / sum(requests)) AS "mean",
    report_duration(min(mean_latency_ns)) AS "lowest interval mean",
    report_duration(max(mean_latency_ns)) AS "highest interval mean"
FROM report_http_latency
WHERE requests > 0
GROUP BY node_id, boot_id, operation;

-- report_latency_detail_table: one row per operation and interval with requests.
CREATE OR REPLACE VIEW report_latency_detail_table AS
SELECT
    row_number() OVER (ORDER BY node_id, boot_id, sequence, operation) AS sort_key,
    node_id AS "node",
    report_boot(boot_id) AS "boot",
    sequence AS "seq",
    report_time(time_unix_nano) AS "at (UTC)",
    operation AS "operation",
    requests AS "requests",
    report_duration(duration_ns) AS "total time",
    report_duration(mean_latency_ns) AS "mean"
FROM report_http_latency
WHERE requests > 0;

-- report_in_flight_summary_table: one row per boot, only if any sample caught a
-- request in progress (otherwise the section prints a sentence instead of zeros).
CREATE OR REPLACE VIEW report_in_flight_summary_table AS
SELECT
    row_number() OVER (ORDER BY node_id, boot_id) AS sort_key,
    node_id AS "node",
    report_boot(boot_id) AS "boot",
    count(*) AS "samples",
    count(*) FILTER (WHERE in_flight > 0) AS "samples > 0",
    max(in_flight) AS "max in flight"
FROM in_flight_v1
WHERE EXISTS (FROM in_flight_v1 WHERE in_flight > 0)
GROUP BY node_id, boot_id;

-- report_in_flight_detail_table: samples that caught a request in progress.
CREATE OR REPLACE VIEW report_in_flight_detail_table AS
SELECT
    row_number() OVER (ORDER BY node_id, boot_id, sequence) AS sort_key,
    node_id AS "node",
    report_boot(boot_id) AS "boot",
    sequence AS "seq",
    report_time(time_unix_nano) AS "at (UTC)",
    in_flight AS "in flight"
FROM in_flight_v1
WHERE in_flight > 0;

-- ===================================================================================
-- Report
-- ===================================================================================

.print
.print CSV METRICS REPORT
.print ==================
.print This report reconstructs what a node did from its finalized CSV metric segments
.print alone: which HTTP requests it served and how fast, how many were in progress, and
.print whether OCC replication discarded any write. Each section states its question, how
.print the numbers are derived, what the columns mean, and then the data.
.print
.print How the data was collected
.print --------------------------
.print A node started with -observability-adapter csv takes a snapshot of every metric once
.print per -observability-interval (the first right after startup) and writes it atomically
.print as one segment file, metrics-*.csv: one row per metric series, all sharing the
.print "snapshot's node_id, region, boot_id, sequence and time_unix_nano."
.print "  - boot_id is 128 random bits chosen when the process starts (shown as its first 8"
.print "    hex digits). A restart gets a new boot_id, so each boot is a separate series."
.print "  - sequence numbers the snapshots of one boot 1, 2, 3, ... keep_files retention"
.print "    deletes the oldest segments, so a directory may start above sequence 1."
.print "  - Counters are cumulative since the boot started. The report subtracts the previous"
.print "    snapshot of the same boot to get per-interval numbers, so the first snapshot"
.print "    present for each boot has no interval value (delta is NULL in the *_v1 views)."
.print "  - Gauges are point-in-time readings taken at the snapshot instant."
.print

SELECT printf('Input: %s (unfinished .tmp files are ignored) = %s, %s, %s.',
        report_count((SELECT count(*) FROM glob('*.csv')), 'segment file', 'segment files'),
        report_count((SELECT count(*) FROM report_snapshots), 'snapshot', 'snapshots'),
        report_count(count(*), 'metric row', 'metric rows'),
        report_count(count(DISTINCT component), 'component', 'components'))
FROM snapshots_v1;

SELECT printf('%s, %s. %s',
        report_count(count(DISTINCT node_id), 'node', 'nodes'),
        report_count(count(*), 'boot', 'boots'),
        CASE WHEN count(median_gap_ns) = 0
            THEN 'Each boot has one snapshot, so no collection interval can be measured.'
            ELSE printf('The median gap between consecutive snapshots is %d ms: the collection interval.',
                round(median(median_gap_ns) / 1e6)::BIGINT)
        END)
FROM report_boots;

SELECT printf('Boot %s of %s starts at sequence %d: retention removed its earlier segments.',
        report_boot(boot_id), node_id, first_sequence)
FROM report_boots
WHERE first_sequence > 1
ORDER BY node_id, boot_id;

FROM report_legend('report_boots_table', 'Sources, one row per boot:', [
    ['node', 'node ID (-id)'],
    ['region', 'region the node reports'],
    ['boot', 'first 8 hex digits of boot_id'],
    ['snapshots', 'number of snapshots (segment files) present'],
    ['sequences', 'first and last sequence number present'],
    ['process start (UTC)', 'when this boot started'],
    ['first snapshot', 'time of the first snapshot present (UTC)'],
    ['span (s)', 'seconds from the first to the last snapshot'],
    ['interval (ms)', 'median gap between consecutive snapshots']
]);
FROM report_table('report_boots_table');

-- -----------------------------------------------------------------------------------
.print
.print 1. OCC last-writer-wins discards
.print --------------------------------
.print Question: did replication throw away any write? When replicas hold concurrent versions
.print of a key, OCC keeps the one with the higher HLC stamp (last writer wins) and counts
.print each dropped version that the winner never saw.
.print How: objstore_occ_lww_discards_total (counter) via occ_discards_v1. Discards in an
.print interval = counter at a snapshot minus counter at the previous snapshot.
.print

SELECT CASE
        WHEN count(*) = 0
            THEN 'No objstore_occ_lww_discards_total samples are present.'
        WHEN coalesce(max(counter_uint64), 0) = 0
            THEN printf('Nothing happened: the counter read 0 in all %s, so no write was discarded.',
                report_count(count(*), 'snapshot', 'snapshots'))
        ELSE printf('Discards happened: %d since boot at the last snapshot. %s with no new discards %s hidden.',
            sum(counter_uint64) FILTER (WHERE is_last),
            report_count(count(*) FILTER (WHERE NOT shown), 'snapshot', 'snapshots'),
            CASE WHEN count(*) FILTER (WHERE NOT shown) = 1 THEN 'is' ELSE 'are' END)
    END
FROM (
    SELECT *,
        sequence = max(sequence) OVER (PARTITION BY node_id, boot_id) AS is_last,
        coalesce(delta > 0 OR (delta IS NULL AND counter_uint64 > 0), false) AS shown
    FROM occ_discards_v1
);
FROM report_legend('report_occ_table', 'Snapshots that recorded discards:', [
    ['node, boot', 'source node and boot'],
    ['seq, at (UTC)', 'snapshot that ends the interval'],
    ['discards', 'discards in the interval (- = first snapshot of the boot)'],
    ['since boot', 'cumulative discards at this snapshot']
]);
FROM report_table('report_occ_table');

-- -----------------------------------------------------------------------------------
.print
.print 2. HTTP requests
.print ----------------
.print Question: which HTTP operations did the node serve, with which status classes, and how
.print many per second?
.print How: objstore_http_requests_total{operation, status_class} (counter; 9 operations x 5
.print status classes = 45 series per snapshot) via counter_deltas_v1, with the arithmetic of
.print http_rates_v1: requests = counter delta; req/s = requests / seconds between snapshots.
.print

SELECT CASE
        WHEN count(*) = 0
            THEN 'No objstore_http_requests_total samples are present.'
        WHEN coalesce(max(since_boot), 0) = 0
            THEN printf('Nothing happened: none of the %d series counted a request in %s.',
                count(DISTINCT series),
                report_count(count(DISTINCT (node_id, boot_id, sequence)), 'snapshot', 'snapshots'))
        WHEN count(DISTINCT series) FILTER (WHERE since_boot > 0) = count(DISTINCT series)
            THEN printf('All %d series counted requests.', count(DISTINCT series))
        ELSE printf('%d of %d series counted requests; the other %d counted none and are not shown.',
            count(DISTINCT series) FILTER (WHERE since_boot > 0),
            count(DISTINCT series),
            count(DISTINCT series) - count(DISTINCT series) FILTER (WHERE since_boot > 0))
    END
FROM (
    SELECT *, (node_id, boot_id, operation, status_class) AS series FROM report_http_requests
);
FROM report_legend('report_http_summary_table', 'Totals per series:', [
    ['node, boot', 'source node and boot'],
    ['operation, status', 'series labels (status = HTTP status class)'],
    ['since boot', 'requests: counter at the last snapshot, including any before the first'],
    ['in window', 'requests: sum of interval deltas from the first to the last snapshot'],
    ['active intervals', 'intervals with at least one request'],
    ['peak req/s', 'requests per second in the busiest interval']
]);
FROM report_table('report_http_summary_table');

FROM report_legend('report_http_detail_table', 'Per interval, only intervals with requests:', [
    ['node, boot', 'source node and boot'],
    ['seq, at (UTC)', 'snapshot that ends the interval'],
    ['operation, status', 'series labels'],
    ['requests', 'requests in the interval (counter delta)'],
    ['interval (ms)', 'milliseconds since the previous snapshot'],
    ['req/s', 'requests / interval']
]);
FROM report_table('report_http_detail_table');
SELECT printf('Not shown: %s with 0 requests, and %s from the first snapshot present for each boot (no previous value).',
        report_count(count(*) FILTER (WHERE requests = 0), 'series-interval', 'series-intervals'),
        report_count(count(*) FILTER (WHERE requests IS NULL), 'row', 'rows'))
FROM report_http_requests
WHERE EXISTS (FROM report_http_detail_table);

-- -----------------------------------------------------------------------------------
.print
.print 3. HTTP latency
.print ---------------
.print Question: how long did the node take to serve each operation?
.print How: objstore_http_request_duration_nanoseconds_total{operation} (handler time, counter)
.print and objstore_http_request_duration_count_total{operation} (requests timed, counter) via
.print http_latency_v1: mean = duration delta / count delta per interval. It is a mean, not a
.print percentile. Each value carries its unit (ns, µs, ms or s).
.print

SELECT CASE
        WHEN count(*) = 0
            THEN 'No HTTP duration samples are present.'
        WHEN coalesce(max(requests), 0) = 0
            THEN printf('Nothing happened: no request was timed in any of %s.',
                report_count(count(DISTINCT (node_id, boot_id, sequence)) FILTER (WHERE requests IS NOT NULL),
                    'interval', 'intervals'))
        ELSE printf('%s timed %s in %s.',
            report_count(count(DISTINCT operation) FILTER (WHERE requests > 0), 'operation', 'operations'),
            report_count(sum(requests)::BIGINT, 'request', 'requests'),
            report_count(count(DISTINCT (node_id, boot_id, sequence)) FILTER (WHERE requests > 0),
                'interval', 'intervals'))
    END
FROM report_http_latency;
FROM report_legend('report_latency_summary_table', 'Per operation:', [
    ['node, boot', 'source node and boot'],
    ['operation', 'operation label'],
    ['requests', 'requests timed from the first to the last snapshot'],
    ['active intervals', 'intervals with at least one timed request'],
    ['mean', 'total handler time / requests over all those intervals'],
    ['lowest interval mean', 'smallest per-interval mean'],
    ['highest interval mean', 'largest per-interval mean']
]);
FROM report_table('report_latency_summary_table');

FROM report_legend('report_latency_detail_table', 'Per interval, only intervals with requests:', [
    ['node, boot', 'source node and boot'],
    ['seq, at (UTC)', 'snapshot that ends the interval'],
    ['operation', 'operation label'],
    ['requests', 'requests timed in the interval (count delta)'],
    ['total time', 'handler time spent on them (duration delta)'],
    ['mean', 'total time / requests']
]);
FROM report_table('report_latency_detail_table');
SELECT printf('Not shown: %s with no timed request, and %s from the first snapshot present for each boot (no previous value).',
        report_count(count(*) FILTER (WHERE requests = 0), 'operation-interval', 'operation-intervals'),
        report_count(count(*) FILTER (WHERE requests IS NULL), 'row', 'rows'))
FROM report_http_latency
WHERE EXISTS (FROM report_latency_detail_table);

-- -----------------------------------------------------------------------------------
.print
.print 4. HTTP requests in flight
.print --------------------------
.print Question: was the node serving requests at the moment snapshots were taken?
.print How: objstore_http_requests_in_flight (gauge) via in_flight_v1. It rises when a handler
.print starts and falls when it returns. It is only read at the snapshot instant, so a short
.print request between two snapshots does not appear here (section 2 counts it).
.print

SELECT CASE
        WHEN count(*) = 0
            THEN 'No objstore_http_requests_in_flight samples are present.'
        WHEN coalesce(max(in_flight), 0) = 0
            THEN printf('Nothing in flight: all %s read 0, so no request overlapped a snapshot.',
                report_count(count(*), 'sample', 'samples'))
        ELSE printf('%d of %s caught requests in progress.',
            count(*) FILTER (WHERE in_flight > 0), report_count(count(*), 'sample', 'samples'))
    END
FROM in_flight_v1;
FROM report_legend('report_in_flight_summary_table', 'Per boot:', [
    ['node, boot', 'source node and boot'],
    ['samples', 'snapshots that include the gauge'],
    ['samples > 0', 'snapshots that caught at least one request in progress'],
    ['max in flight', 'highest reading (requests)']
]);
FROM report_table('report_in_flight_summary_table');

FROM report_legend('report_in_flight_detail_table', 'Samples above 0 (samples that read 0 are not shown):', [
    ['node, boot', 'source node and boot'],
    ['seq, at (UTC)', 'snapshot'],
    ['in flight', 'requests in progress at that instant']
]);
FROM report_table('report_in_flight_detail_table');
