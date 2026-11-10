-- 0001 · X1d 物理搬迁：public.* → market.* / ingest.*
--
-- 背景：本仓 DDL（`internal/schema.Statements()`）按目标态把 22 张表建在
-- `market` / `ingest`；core 时代用裸表名写、仓内无 search_path，真实数据落在
-- `public.*` 的同名表里。本脚本把这批表的物理位置搬过去，不自造任何一张新表。
--
-- 前置（不满足就别跑，见 docs/db-schema.md §1.5）：
--   1. core 写点已归零/删除（`SVC-CORE-DATA-RETIRE`，已删 `pkg/data/**`）；
--   2. 本仓 DDL 已执行过（`market` / `ingest` 存在，空壳副本已在目标 schema 里）；
--   3. 搬家期间无写入：脚本在单事务里做完全部移动，失败整体回滚。
--
-- 冲突策略：
--   - `public.x` 不存在 ⇒ 跳过（已搬过，或从来没建过）；
--   - 目标 `market.x` / `ingest.x` 已存在且**为空** ⇒ 删掉空壳再搬（DDL 先建的空表）；
--   - 目标已存在且**有行** ⇒ 直接报错中止，交人工判断（绝不静默丢数据）。
--
-- 回滚：`0001_x1d_set_schema.down.sql`（同样幂等，按相反方向搬回 public）。
-- 用法：psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/0001_x1d_set_schema.sql
--
-- 表清单必须与 `internal/schema.TableNames()` 逐条一致；
-- 守卫测试 `TestMigrationTableListMatchesSchema` 会在漂移时变红。

BEGIN;

DO $$
DECLARE
    mapping record;
    existing_rows bigint;
BEGIN
    IF to_regnamespace('market') IS NULL OR to_regnamespace('ingest') IS NULL THEN
        RAISE EXCEPTION 'market / ingest schema 不存在：先让本仓 DDL 跑过一遍（cmd/dataservice 启动即执行），再搬';
    END IF;

    FOR mapping IN
        SELECT * FROM (VALUES
            ('ingest', 'data_migrations'),
            ('ingest', 'raw'),
            ('ingest', 'sync_jobs'),
            ('ingest', 'sync_schedules'),
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
        ) AS v(target_schema, table_name)
    LOOP
        IF to_regclass(format('public.%I', mapping.table_name)) IS NULL THEN
            RAISE NOTICE 'skip  public.%(不存在/已搬过)', mapping.table_name;
            CONTINUE;
        END IF;

        IF to_regclass(format('%I.%I', mapping.target_schema, mapping.table_name)) IS NOT NULL THEN
            EXECUTE format('SELECT count(*) FROM %I.%I', mapping.target_schema, mapping.table_name)
                INTO existing_rows;
            IF existing_rows <> 0 THEN
                RAISE EXCEPTION
                    '目标表 %.% 已有 % 行，public.% 也还在：两边都有数据，拒绝自动合并',
                    mapping.target_schema, mapping.table_name, existing_rows, mapping.table_name;
            END IF;
            EXECUTE format('DROP TABLE %I.%I', mapping.target_schema, mapping.table_name);
            RAISE NOTICE 'drop  空壳 %.%（随后由 public 搬入）', mapping.target_schema, mapping.table_name;
        END IF;

        EXECUTE format('ALTER TABLE public.%I SET SCHEMA %I',
                       mapping.table_name, mapping.target_schema);
        RAISE NOTICE 'move  public.% -> %.%', mapping.table_name, mapping.target_schema, mapping.table_name;
    END LOOP;
END
$$;

COMMIT;
