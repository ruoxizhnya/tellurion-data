-- 0001 的执行后对拍：每张表落在哪个 schema、各多少行。
--
-- 搬迁不该改变行数（ALTER TABLE ... SET SCHEMA 不复制数据），所以最有力的
-- 对拍方式是「搬前跑一遍记下计数 → 搬后再跑一遍逐表比对」。本脚本输出
-- 可直接贴进迁移记录。
--
-- 用法：psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/0001_x1d_set_schema.verify.sql

WITH expected(target_schema, table_name) AS (
    VALUES
        ('ingest', 'data_migrations'),
        ('ingest', 'raw'),
        ('ingest', 'sync_jobs'),
        ('ingest', 'sync_schedules'),
        ('market', 'adjust_baseline_changes'),
        ('market', 'announcements'),
        ('market', 'capital_flow'),
        ('market', 'dividends'),
        ('market', 'global_ohlcv'),
        ('market', 'hot_search'),
        ('market', 'index_constituents'),
        ('market', 'limit_up_pool'),
        ('market', 'market_activity'),
        ('market', 'news'),
        ('market', 'ohlcv_daily_qfq'),
        ('market', 'ohlcv_minute'),
        ('market', 'realtime_quote'),
        ('market', 'sectors'),
        ('market', 'splits'),
        ('market', 'stock_sector_map'),
        ('market', 'stocks'),
        ('market', 'top_list'),
        ('market', 'trading_calendar')
)
SELECT e.target_schema,
       e.table_name,
       CASE
           WHEN to_regclass(format('%I.%I', e.target_schema, e.table_name)) IS NOT NULL THEN 'ok'
           ELSE 'MISSING'
       END AS target_state,
       CASE
           WHEN to_regclass(format('public.%I', e.table_name)) IS NOT NULL THEN 'still-in-public'
           ELSE ''
       END AS public_state
FROM expected e
ORDER BY e.target_schema, e.table_name;

-- 行数（逐表精确 count，不用 reltuples —— 它只是估算值，对拍不能拿估算值）：
DO $$
DECLARE
    mapping record;
    n bigint;
BEGIN
    FOR mapping IN
        SELECT * FROM (VALUES
            ('ingest', 'data_migrations'), ('ingest', 'raw'),
            ('ingest', 'sync_jobs'), ('ingest', 'sync_schedules'),
            ('market', 'adjust_baseline_changes'),
            ('market', 'announcements'), ('market', 'capital_flow'),
            ('market', 'dividends'), ('market', 'global_ohlcv'),
            ('market', 'hot_search'), ('market', 'index_constituents'),
            ('market', 'limit_up_pool'), ('market', 'market_activity'),
            ('market', 'news'), ('market', 'ohlcv_daily_qfq'),
            ('market', 'ohlcv_minute'), ('market', 'realtime_quote'),
            ('market', 'sectors'), ('market', 'splits'),
            ('market', 'stock_sector_map'), ('market', 'stocks'),
            ('market', 'top_list'), ('market', 'trading_calendar')
        ) AS v(target_schema, table_name)
    LOOP
        IF to_regclass(format('%I.%I', mapping.target_schema, mapping.table_name)) IS NULL THEN
            RAISE NOTICE 'count %.- = MISSING', mapping.table_name;
            CONTINUE;
        END IF;
        EXECUTE format('SELECT count(*) FROM %I.%I', mapping.target_schema, mapping.table_name) INTO n;
        RAISE NOTICE 'count %.% = %', mapping.target_schema, mapping.table_name, n;
    END LOOP;
END
$$;

-- `visible_at` 回填另跑（`schema.BackfillStatements()`，有 data_migrations 哨兵防重放，
-- 见 docs/db-schema.md §6.5 D2）。本脚本只回答「搬没搬、搬丢没丢」。
