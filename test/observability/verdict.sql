-- Shared verdict for the CSV check scripts (check-objnode-csv.sh, check-csv.sh).
--
-- Each script builds assert.sql as: its own intro, deploy/observability/queries.sql
-- (the report), a view named checks, then this file. checks has one row per claim:
--   n        INTEGER  display order
--   claim    VARCHAR  what the test asserts, in words
--   observed VARCHAR  the value computed from the CSV data
--   expected VARCHAR  the condition that value must meet
--   pass     BOOLEAN  whether it does (NULL counts as a failure)
-- The verdict table prints before the failing statement, so a failure is explained;
-- error() under duckdb -bail then makes query-csv.sh exit non-zero.

CREATE OR REPLACE VIEW report_verdict_table AS
SELECT
    n AS sort_key,
    n AS "#",
    claim AS "claim",
    observed AS "observed",
    expected AS "expected",
    CASE WHEN coalesce(pass, false) THEN 'PASS' ELSE 'FAIL' END AS "result"
FROM checks;

.print
.print VERDICT
.print =======
.print Each row is one claim this test makes about the CSV data, checked by a query over the
.print *_v1 views. The test fails, and exits non-zero, if any row is FAIL.
FROM report_legend('report_verdict_table', 'Checks:', [
    ['#', 'check number'],
    ['claim', 'what the test asserts'],
    ['observed', 'value computed from the CSV data'],
    ['expected', 'condition the observed value must meet'],
    ['result', 'PASS or FAIL']
]);
FROM report_table('report_verdict_table');
.print

SELECT CASE
        WHEN count(*) = 0 THEN 'FAIL: no checks were defined.'
        WHEN bool_and(coalesce(pass, false)) THEN printf('PASS: all %d checks passed.', count(*))
        ELSE printf('FAIL: %d of %d checks failed: %s.',
            count(*) FILTER (WHERE NOT coalesce(pass, false)), count(*),
            string_agg(printf('#%d %s', n, claim), '; ' ORDER BY n)
                FILTER (WHERE NOT coalesce(pass, false)))
    END
FROM checks;

-- Fail closed: stop with an error (non-zero exit under -bail) unless every check passed.
.mode trash
SELECT CASE
        WHEN count(*) > 0 AND bool_and(coalesce(pass, false)) THEN true
        ELSE error(printf('%d of %d CSV checks failed', count(*) FILTER (WHERE NOT coalesce(pass, false)), count(*)))
    END
FROM checks;
.mode list
