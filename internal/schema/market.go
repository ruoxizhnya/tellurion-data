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
			status VARCHAR(20) DEFAULT 'active',
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,

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
		`CREATE TABLE IF NOT EXISTS market.trading_calendar (
			trade_date DATE PRIMARY KEY,
			exchange VARCHAR(10) DEFAULT 'SSE',
			is_trading_day BOOLEAN DEFAULT TRUE
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

		// 指数日线（实测漏项：不在 core 的 TableMapper 13 个映射里）
		`CREATE TABLE IF NOT EXISTS market.index_daily (
			code        VARCHAR(20)  NOT NULL,
			trade_date  DATE         NOT NULL,
			open        DOUBLE PRECISION,
			high        DOUBLE PRECISION,
			low         DOUBLE PRECISION,
			close       DOUBLE PRECISION NOT NULL,
			pre_close   DOUBLE PRECISION,
			change      DOUBLE PRECISION,
			pct_chg     DOUBLE PRECISION,
			volume      DOUBLE PRECISION NOT NULL DEFAULT 0,
			turnover    DOUBLE PRECISION,
			source      VARCHAR(32)  NOT NULL,
			as_of       TIMESTAMPTZ  NOT NULL,
			PRIMARY KEY (code, trade_date)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_market_index_daily_date ON market.index_daily (trade_date DESC)`,
	}
}
