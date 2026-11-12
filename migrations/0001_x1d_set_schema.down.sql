-- 0001 的 rollback：market.* / ingest.* → public.*
--
-- 用途：搬迁后发现必须回退（例如 core 侧还有没停干的写点、或读面变空）。
-- 语义与 up 对称且幂等：
--   - 源表不存在 ⇒ 跳过；
--   - `public.x` 已存在且为空 ⇒ 删空壳再搬；有行 ⇒ 报错中止；
--   - 单事务，失败整体回滚。
--
-- 注意：`ingest.raw` 与 `market.index_daily` 的所有权历史不同——本脚本只搬
-- `TableNames()` 里那 22 张；`market.index_daily` 不归本仓，永远不动。
--
-- 用法：psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/0001_x1d_set_schema.down.sql

BEGIN;

DO $$
DECLARE
    mapping record;
    existing_rows bigint;
BEGIN
    FOR mapping IN
        SELECT * FROM (VALUES
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
            ('market', 'stock_fundamentals'),
            ('market', 'stock_sector_map'),
            ('market', 'stocks'),
            ('market', 'top_list'),
            ('market', 'trading_calendar')
        ) AS v(source_schema, table_name)
    LOOP
        IF to_regclass(format('%I.%I', mapping.source_schema, mapping.table_name)) IS NULL THEN
            RAISE NOTICE 'skip  %.%(不存在/已回退)', mapping.source_schema, mapping.table_name;
            CONTINUE;
        END IF;

        IF to_regclass(format('public.%I', mapping.table_name)) IS NOT NULL THEN
            EXECUTE format('SELECT count(*) FROM public.%I', mapping.table_name) INTO existing_rows;
            IF existing_rows <> 0 THEN
                RAISE EXCEPTION
                    'public.% 已有 % 行，%.% 也还在：两边都有数据，拒绝自动合并',
                    mapping.table_name, existing_rows, mapping.source_schema, mapping.table_name;
            END IF;
            EXECUTE format('DROP TABLE public.%I', mapping.table_name);
            RAISE NOTICE 'drop  空壳 public.%', mapping.table_name;
        END IF;

        EXECUTE format('ALTER TABLE %I.%I SET SCHEMA public',
                       mapping.source_schema, mapping.table_name);
        RAISE NOTICE 'move  %.% -> public.%', mapping.source_schema, mapping.table_name, mapping.table_name;
    END LOOP;
END
$$;

COMMIT;
