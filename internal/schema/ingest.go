package schema

// IngestStatements 返回 `ingest` schema 的表结构（有序、幂等）。
//
// 承载的是**原始落库与同步编排**（`docs/db-schema.md` §1.5）：未归一化原文
// 与作业/调度/迁移留痕。它不是观测数据流，因此不在可见性闸门覆盖面内
// （§6.2 C 集），本文件也不给它加 `visible_at`。
func IngestStatements() []string {
	return []string{
		`CREATE SCHEMA IF NOT EXISTS ingest`,

		// 原始响应归档（content_hash 内容寻址）
		`CREATE TABLE IF NOT EXISTS ingest.raw (
			content_hash VARCHAR(64) PRIMARY KEY,
			source VARCHAR(64) NOT NULL,
			dataset VARCHAR(128) NOT NULL,
			key TEXT NOT NULL,
			as_of DATE,
			payload JSONB NOT NULL,
			fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_raw_source_dataset ON ingest.raw(source, dataset)`,
		`CREATE INDEX IF NOT EXISTS idx_ingest_raw_as_of ON ingest.raw(as_of DESC)`,

		// 同步作业队列
		`CREATE TABLE IF NOT EXISTS ingest.sync_jobs (
			id              VARCHAR(64) PRIMARY KEY,
			job_type        VARCHAR(50) NOT NULL,
			status          VARCHAR(20) NOT NULL DEFAULT 'pending',
			params          JSONB NOT NULL DEFAULT '{}',
			progress_percent INT NOT NULL DEFAULT 0,
			total_items     INT NOT NULL DEFAULT 0,
			processed_items INT NOT NULL DEFAULT 0,
			failed_items    INT NOT NULL DEFAULT 0,
			error_message   TEXT,
			result          JSONB,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			started_at      TIMESTAMPTZ,
			completed_at    TIMESTAMPTZ,
			retry_count     INT NOT NULL DEFAULT 0,
			max_retries     INT NOT NULL DEFAULT 3,
			scheduled_at    TIMESTAMPTZ,
			worker_id       VARCHAR(50)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_jobs_status ON ingest.sync_jobs(status)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_jobs_type ON ingest.sync_jobs(job_type)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_jobs_created_at ON ingest.sync_jobs(created_at DESC)`,

		// cron 定时同步
		`CREATE TABLE IF NOT EXISTS ingest.sync_schedules (
			id              SERIAL PRIMARY KEY,
			name            VARCHAR(100) NOT NULL UNIQUE,
			description     TEXT,
			job_type        VARCHAR(50) NOT NULL,
			cron_expression VARCHAR(100) NOT NULL,
			params          JSONB NOT NULL DEFAULT '{}',
			is_active       BOOLEAN NOT NULL DEFAULT TRUE,
			last_run_at     TIMESTAMPTZ,
			last_run_status VARCHAR(20),
			last_run_job_id VARCHAR(64),
			next_run_at     TIMESTAMPTZ,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			created_by      VARCHAR(50) DEFAULT 'system'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_schedules_active ON ingest.sync_schedules(is_active)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_schedules_next_run ON ingest.sync_schedules(next_run_at)`,

		// 迁移留痕（`docs/db-schema.md` §6.5 D2 的哨兵表）
		`CREATE TABLE IF NOT EXISTS ingest.data_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	}
}
