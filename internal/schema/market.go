package schema

// marketSchema 是目标 schema 名。今天多数表物理在 `public`（`docs/db-schema.md`
// §1 注「迁移不是改名」），本文件是**归属声明**：这些表由本仓建、由本仓写。
const marketSchema = "market"

// MarketStatements 返回 `market` schema 的表结构（有序、幂等）。
//
// 列形状逐表对齐 core 的内联 DDL（`tellurion:pkg/storage/postgres.go` 的
// `migrationStatements()`，2026-10-24 实测），只把无前缀表换到 `market.`。
// 不新增列、不改类型——语义变更属另一个单，不夹在归属迁移里做。
func MarketStatements() []string {
	return []string{
		`CREATE SCHEMA IF NOT EXISTS market`,

		// 标的（内外部代码映射）
		`CREATE TABLE IF NOT EXISTS market.stocks (
			symbol VARCHAR(20) PRIMARY KEY,
			name VARCHAR(200) NOT NULL,
			exchange VARCHAR(20) NOT NULL,
			industry VARCHAR(100),
			market_cap DOUBLE PRECISION,
			list_date DATE,
			delist_date DATE,
			status VARCHAR(20) DEFAULT 'active',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// 既有库没有通用 ALTER 迁移通道：这条幂等加列让 `market.stocks` 前滚
		// 到带 delist_date 的形状（ADR-0009 决策二）。
		`ALTER TABLE IF EXISTS market.stocks ADD COLUMN IF NOT EXISTS delist_date DATE`,

		// 日线（前复权，唯一进回测口径）
		`CREATE TABLE IF NOT EXISTS market.ohlcv_daily_qfq (
			symbol VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			open DOUBLE PRECISION NOT NULL,
			high DOUBLE PRECISION NOT NULL,
			low DOUBLE PRECISION NOT NULL,
			close DOUBLE PRECISION NOT NULL,
			volume DOUBLE PRECISION NOT NULL,
			turnover DOUBLE PRECISION DEFAULT 0,
			trade_days INT DEFAULT 0,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (symbol, trade_date)
		)`,

		// 复权基准账本（append-only，ADR-0011）
		//
		// qfq 的基准日/基准因子必须**全局固定**：同一 (symbol, trade_date) 的
		// close 不许随「当初用哪个窗口拉的」变化。每次建立（bootstrap）或人工
		// 改基准（manual）都追加一行，`previous_*` 记录变更前的基准。
		// 不设 updated_at / 不许 UPDATE：账本只增不改，审计靠这条约束。
		`CREATE TABLE IF NOT EXISTS market.adjust_baseline_changes (
			id BIGSERIAL PRIMARY KEY,
			symbol VARCHAR(20) NOT NULL,
			base_date DATE NOT NULL,
			base_factor DOUBLE PRECISION NOT NULL,
			previous_date DATE,
			previous_factor DOUBLE PRECISION,
			reason VARCHAR(16) NOT NULL,
			changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT ck_adjust_baseline_reason CHECK (reason IN ('bootstrap', 'manual')),
			CONSTRAINT ck_adjust_baseline_factor CHECK (base_factor > 0)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_adjust_baseline_symbol_changed
			ON market.adjust_baseline_changes (symbol, changed_at DESC, id DESC)`,
		// 每只票最多一条 bootstrap：两个 worker 同时首拉同一只票时，
		// 并发的「建基准」只能有一个赢家，另一个必须读回赢家的基准。
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_adjust_baseline_bootstrap
			ON market.adjust_baseline_changes (symbol) WHERE reason = 'bootstrap'`,

		// 财报（C 集：可见性判据走 ann_date，`available_date` 是生成列）。
		// 2026-11-12 归属裁决：本仓成为唯一写者（`public.stock_fundamentals`
		// 由 core 的 DDL 持有、写点已随 pkg/data 退役归零，待 core 切读本表）。
		// 列形状逐字对齐 core 的 public 表：`ts_code + trade_date` 唯一，
		// `available_date = COALESCE(ann_date, trade_date)` —— 三季报 9/30 截止、
		// 10/25 披露，PIT 只认 10/25。
		`CREATE TABLE IF NOT EXISTS market.stock_fundamentals (
			ts_code VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			ann_date DATE,
			end_date DATE,
			pe DOUBLE PRECISION,
			pb DOUBLE PRECISION,
			ps DOUBLE PRECISION,
			roe DOUBLE PRECISION,
			roa DOUBLE PRECISION,
			debt_to_equity DOUBLE PRECISION,
			gross_margin DOUBLE PRECISION,
			net_margin DOUBLE PRECISION,
			revenue DOUBLE PRECISION,
			net_profit DOUBLE PRECISION,
			total_assets DOUBLE PRECISION,
			total_liab DOUBLE PRECISION,
			available_date DATE GENERATED ALWAYS AS (COALESCE(ann_date, trade_date)) STORED,
			CONSTRAINT pk_stock_fundamentals PRIMARY KEY (ts_code, trade_date)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_stock_fundamentals_available
			ON market.stock_fundamentals (ts_code, available_date DESC)`,

		// 分钟线
		`CREATE TABLE IF NOT EXISTS market.ohlcv_minute (
			symbol      VARCHAR(20) NOT NULL,
			ts          TIMESTAMPTZ NOT NULL,
			open        DOUBLE PRECISION NOT NULL,
			high        DOUBLE PRECISION NOT NULL,
			low         DOUBLE PRECISION NOT NULL,
			close       DOUBLE PRECISION NOT NULL,
			volume      BIGINT,
			amount      DOUBLE PRECISION,
			source      VARCHAR(32) DEFAULT 'mootdx',
			ingest_time TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (symbol, ts)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ohlcv_minute_symbol_ts ON market.ohlcv_minute (symbol, ts DESC)`,

		// 实时快照（五档）
		`CREATE TABLE IF NOT EXISTS market.realtime_quote (
			symbol      VARCHAR(20) NOT NULL,
			ts          TIMESTAMPTZ NOT NULL,
			price       DOUBLE PRECISION,
			open        DOUBLE PRECISION,
			high        DOUBLE PRECISION,
			low         DOUBLE PRECISION,
			last_close  DOUBLE PRECISION,
			volume      BIGINT,
			amount      DOUBLE PRECISION,
			bid1        DOUBLE PRECISION, ask1 DOUBLE PRECISION,
			bid1_vol    INT, ask1_vol INT,
			bid2        DOUBLE PRECISION, ask2 DOUBLE PRECISION,
			bid2_vol    INT, ask2_vol INT,
			bid3        DOUBLE PRECISION, ask3 DOUBLE PRECISION,
			bid3_vol    INT, ask3_vol INT,
			bid4        DOUBLE PRECISION, ask4 DOUBLE PRECISION,
			bid4_vol    INT, ask4_vol INT,
			bid5        DOUBLE PRECISION, ask5 DOUBLE PRECISION,
			bid5_vol    INT, ask5_vol INT,
			source      VARCHAR(32) DEFAULT 'mootdx',
			ingest_time TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (symbol, ts)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_realtime_quote_symbol_ts ON market.realtime_quote (symbol, ts DESC)`,

		// 资金流
		`CREATE TABLE IF NOT EXISTS market.capital_flow (
			id               BIGSERIAL PRIMARY KEY,
			symbol           VARCHAR(20) NOT NULL,
			trade_date       DATE NOT NULL,
			period           VARCHAR(16) NOT NULL,
			main_net         DOUBLE PRECISION,
			main_buy_amount  DOUBLE PRECISION,
			main_sell_amount DOUBLE PRECISION,
			super_net        DOUBLE PRECISION,
			large_net        DOUBLE PRECISION,
			medium_net       DOUBLE PRECISION,
			small_net        DOUBLE PRECISION,
			main_net_ratio   DOUBLE PRECISION,
			retail_net       DOUBLE PRECISION,
			retail_net_ratio DOUBLE PRECISION,
			close_price      DOUBLE PRECISION,
			change_pct       DOUBLE PRECISION,
			source           VARCHAR(32) DEFAULT 'eastmoney',
			ingest_time      TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE (symbol, trade_date, period)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cf_symbol_date ON market.capital_flow (symbol, trade_date DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_cf_date_main_net ON market.capital_flow (trade_date DESC, main_net DESC)`,

		// 行情面估值（C 集：粒度是「每个交易日一个截面值」，与财报的
		// 「每个报告期一个人点值」分表，见 ADR-0012 追加节）。
		// PIT 判据是 `trade_date`（收盘后可见），不是公告日；因此本表没有
		// `ann_date`，也不加 `visible_at`（同表两个锚点会漂移）。
		// 列只取 Tushare `daily_basic` 里本仓确实要用的估值字段，多余的不塞。
		`CREATE TABLE IF NOT EXISTS market.daily_basic (
			ts_code    VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			pe         DOUBLE PRECISION,
			pe_ttm     DOUBLE PRECISION,
			pb         DOUBLE PRECISION,
			ps         DOUBLE PRECISION,
			ps_ttm     DOUBLE PRECISION,
			dv_ratio   DOUBLE PRECISION,
			total_mv   DOUBLE PRECISION,
			circ_mv    DOUBLE PRECISION,
			CONSTRAINT pk_daily_basic PRIMARY KEY (ts_code, trade_date)
		)`,

		// 板块快照
		`CREATE TABLE IF NOT EXISTS market.sectors (
			sector_code     VARCHAR(32) PRIMARY KEY,
			sector_name     VARCHAR(64) NOT NULL,
			category        VARCHAR(32) NOT NULL DEFAULT 'industry',
			trade_date      DATE NOT NULL,
			change_pct      DOUBLE PRECISION,
			leading_symbol  VARCHAR(20),
			leading_change  DOUBLE PRECISION,
			source          VARCHAR(32) DEFAULT 'eastmoney',
			ingest_time     TIMESTAMPTZ DEFAULT NOW(),
			data_version    INT DEFAULT 1
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sectors_date ON market.sectors (trade_date DESC)`,

		// 个股 ↔ 板块
		`CREATE TABLE IF NOT EXISTS market.stock_sector_map (
			symbol      VARCHAR(20) NOT NULL,
			sector_code VARCHAR(32) NOT NULL,
			sector_name VARCHAR(64) NOT NULL,
			source      VARCHAR(32) DEFAULT 'eastmoney',
			ingest_time TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (symbol, sector_code)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ssm_sector ON market.stock_sector_map (sector_code)`,

		// 龙虎榜
		`CREATE TABLE IF NOT EXISTS market.top_list (
			id          BIGSERIAL PRIMARY KEY,
			trade_date  DATE NOT NULL,
			symbol      VARCHAR(20) NOT NULL,
			name        VARCHAR(100) NOT NULL,
			net_buy     DOUBLE PRECISION,
			buy_amount  DOUBLE PRECISION,
			sell_amount DOUBLE PRECISION,
			turnover    DOUBLE PRECISION,
			reason      TEXT,
			explain     TEXT,
			close_price DOUBLE PRECISION,
			change_pct  DOUBLE PRECISION,
			source      VARCHAR(32) DEFAULT 'eastmoney',
			ingest_time TIMESTAMPTZ DEFAULT NOW(),
			data_version INT DEFAULT 1,
			UNIQUE (trade_date, symbol)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_top_list_date ON market.top_list (trade_date DESC)`,

		// 涨停池
		`CREATE TABLE IF NOT EXISTS market.limit_up_pool (
			id          BIGSERIAL PRIMARY KEY,
			trade_date  DATE NOT NULL,
			symbol      VARCHAR(20) NOT NULL,
			name        VARCHAR(100) NOT NULL,
			limit_price DOUBLE PRECISION,
			first_time  TIMESTAMPTZ,
			last_time   TIMESTAMPTZ,
			limit_times INT DEFAULT 1,
			continuous  INT DEFAULT 1,
			industry    VARCHAR(64),
			concept     TEXT,
			amount      DOUBLE PRECISION,
			source      VARCHAR(32) DEFAULT 'eastmoney',
			ingest_time TIMESTAMPTZ DEFAULT NOW(),
			data_version INT DEFAULT 1,
			UNIQUE (trade_date, symbol)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_limit_up_date ON market.limit_up_pool (trade_date DESC)`,

		// 公告
		`CREATE TABLE IF NOT EXISTS market.announcements (
			ann_id       VARCHAR(64) PRIMARY KEY,
			symbol       VARCHAR(20) NOT NULL,
			ann_title    TEXT NOT NULL,
			ann_time     TIMESTAMPTZ NOT NULL,
			ann_type     VARCHAR(64),
			pdf_url      TEXT,
			source       VARCHAR(32) DEFAULT 'juchao',
			ingest_time  TIMESTAMPTZ DEFAULT NOW(),
			data_version INT DEFAULT 1
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ann_time ON market.announcements (ann_time DESC)`,

		// 新闻
		`CREATE TABLE IF NOT EXISTS market.news (
			news_id      VARCHAR(64) PRIMARY KEY,
			symbol       VARCHAR(20) NOT NULL,
			title        TEXT NOT NULL,
			content      TEXT,
			publish_time TIMESTAMPTZ NOT NULL,
			url          TEXT,
			source_name  VARCHAR(64),
			source       VARCHAR(32) DEFAULT 'eastmoney',
			ingest_time  TIMESTAMPTZ DEFAULT NOW(),
			data_version INT DEFAULT 1
		)`,
		`CREATE INDEX IF NOT EXISTS idx_news_time ON market.news (publish_time DESC)`,

		// 热搜
		`CREATE TABLE IF NOT EXISTS market.hot_search (
			id            BIGSERIAL PRIMARY KEY,
			rank          INT NOT NULL,
			keyword       VARCHAR(128) NOT NULL,
			snapshot_time TIMESTAMPTZ NOT NULL,
			heat          DOUBLE PRECISION,
			source        VARCHAR(32) DEFAULT 'xueqiu',
			ingest_time   TIMESTAMPTZ DEFAULT NOW(),
			data_version  INT DEFAULT 1,
			UNIQUE (rank, snapshot_time)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_hot_search_time ON market.hot_search (snapshot_time DESC)`,

		// 境外日线
		`CREATE TABLE IF NOT EXISTS market.global_ohlcv (
			symbol       VARCHAR(20) NOT NULL,
			trade_date   DATE NOT NULL,
			open         DOUBLE PRECISION NOT NULL,
			high         DOUBLE PRECISION NOT NULL,
			low          DOUBLE PRECISION NOT NULL,
			close        DOUBLE PRECISION NOT NULL,
			volume       DOUBLE PRECISION,
			adj_close    DOUBLE PRECISION,
			source       VARCHAR(32) DEFAULT 'yahoo_finance',
			ingest_time  TIMESTAMPTZ DEFAULT NOW(),
			data_version INT DEFAULT 1,
			PRIMARY KEY (symbol, trade_date)
		)`,

		// 交易日历（calendar_hash 的输入，不是观测数据流）
		//
		// 单一 A 股日历（ADR-0009）：主键只有 trade_date，没有交易所维度。
		// exchange 只记录「最近写入来源」，不是 key；SSE/SZSE/BSE 交易日一致。
		// 写侧另有混批守卫（store.ErrMixedTradingExchanges）。CHECK 只对新建
		// 表生效——既有库没有 ALTER 迁移，靠写侧守卫。
		`CREATE TABLE IF NOT EXISTS market.trading_calendar (
			trade_date DATE PRIMARY KEY,
			exchange VARCHAR(10) NOT NULL DEFAULT 'SSE',
			is_trading_day BOOLEAN DEFAULT TRUE,
			CONSTRAINT ck_trading_calendar_exchange CHECK (exchange IN ('SSE', 'SZSE', 'BSE'))
		)`,

		// 指数成分（as-of 语义的载体）
		`CREATE TABLE IF NOT EXISTS market.index_constituents (
			id SERIAL PRIMARY KEY,
			index_code VARCHAR(20) NOT NULL,
			symbol VARCHAR(20) NOT NULL,
			in_date DATE,
			out_date DATE,
			weight DOUBLE PRECISION,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_index_constituents_asof
			ON market.index_constituents (index_code, in_date, out_date)`,

		// 分红 / 拆股
		`CREATE TABLE IF NOT EXISTS market.dividends (
			id SERIAL PRIMARY KEY,
			symbol VARCHAR(20) NOT NULL,
			ann_date DATE NOT NULL,
			rec_date DATE,
			pay_date DATE,
			div_amt DOUBLE PRECISION,
			stk_div DOUBLE PRECISION,
			stk_ratio DOUBLE PRECISION,
			cash_ratio DOUBLE PRECISION,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS market.splits (
			id SERIAL PRIMARY KEY,
			symbol VARCHAR(20) NOT NULL,
			trade_date DATE NOT NULL,
			ann_date DATE,
			stk_div_ratio DOUBLE PRECISION,
			cash_div_ratio DOUBLE PRECISION,
			currency VARCHAR(10) DEFAULT 'CNY',
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,

		// 市场活跃度（B 集：已有 as_of，**不新建 visible_at**）
		`CREATE TABLE IF NOT EXISTS market.market_activity (
			id              BIGSERIAL PRIMARY KEY,
			symbol          VARCHAR(20) NOT NULL,
			trade_date      DATE NOT NULL,
			turnover_rate   DOUBLE PRECISION,
			turnover_rate_f DOUBLE PRECISION,
			volume_ratio    DOUBLE PRECISION,
			source          VARCHAR(64) NOT NULL,
			as_of           TIMESTAMPTZ NOT NULL,
			CONSTRAINT market_activity_identity_present CHECK (
				btrim(symbol) <> '' AND btrim(source) <> ''
			),
			UNIQUE (symbol, trade_date, source)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_market_activity_trade_date ON market.market_activity (trade_date)`,
	}
}
