package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"tellurion-data/internal/evidence"
	"tellurion-data/internal/serving"
	"tellurion-data/internal/store"
)

// 本文件是 `ingest.raw` 证据归档面的 HTTP 层（`docs/api-schema.md` §4）：
//
//   - `POST /api/ingest/raw` 写面（单写者 = data）；
//   - `GET /api/evidence/{content_hash}` 按内容哈希查单条；
//   - `GET /api/evidence` 按来源/数据集列最近若干条（分页有硬上限）。
//
// 判定不在本层：脱敏与哈希在 `internal/evidence`，读写与保留期清理在
// `internal/store`。本层只做解码、参数校验与哨兵 → HTTP 状态码映射。

const (
	defaultEvidenceLimit = 20
	maxEvidenceLimit     = 100
)

// EvidenceArchive 是归档面的读/写端口（`*store.EvidenceStore` 实现）。
// nil = 未接线 ⇒ 端点 503，与既有 fail-closed 口径一致。
type EvidenceArchive interface {
	Put(ctx context.Context, rec evidence.Record) (bool, error)
	Get(ctx context.Context, contentHash string) (*evidence.Record, error)
	List(ctx context.Context, filter store.EvidenceFilter) ([]evidence.Record, error)
}

// WithEvidence 装配证据归档面。nil = 未接线。
func WithEvidence(archive EvidenceArchive) Option {
	return func(s *Server) { s.evidence = archive }
}

// handleIngestRaw 是 `POST /api/ingest/raw`：原始报文归档。
//
// 顺序刻意固定为 **脱敏 → 规范化 → 哈希 → 落库**：引用的哈希必须与库里
// 能读到的（已脱敏）内容一致；先哈希再脱敏会让引用查不回来。
func (s *Server) handleIngestRaw(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.evidence == nil {
		writeDependency(w, "evidence archive is not wired (no PG store)")
		return
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngestBodyBytes))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var req evidence.WriteRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, fmt.Errorf("%w: body is not a valid raw ingest request: %v", serving.ErrInvalidParameter, err))
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, fmt.Errorf("%w: %v", serving.ErrInvalidParameter, err))
		return
	}

	payload, err := evidence.Redact(req.Payload)
	if err != nil {
		writeError(w, fmt.Errorf("%w: %v", serving.ErrInvalidParameter, err))
		return
	}
	contentHash, err := evidence.CanonicalHash(payload)
	if err != nil {
		writeError(w, fmt.Errorf("%w: %v", serving.ErrInvalidParameter, err))
		return
	}
	if req.ContentHash != "" && req.ContentHash != contentHash {
		writeError(w, fmt.Errorf("%w: content_hash %s does not match the canonical hash %s",
			serving.ErrInvalidParameter, req.ContentHash, contentHash))
		return
	}

	rec := evidence.Record{
		ContentHash: contentHash,
		Source:      req.Source,
		Dataset:     req.Dataset,
		Key:         req.Key,
		AsOf:        req.AsOf,
		Payload:     payload,
		FetchedAt:   time.Now().UTC(),
	}
	if _, err := s.evidence.Put(r.Context(), rec); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

// handleGetEvidence 是 `GET /api/evidence/{content_hash}`。
//
// 不存在 ⇒ 404 `evidence_not_found`（「未摄取」是明确状态，不是 500）。
func (s *Server) handleGetEvidence(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.evidence == nil {
		writeDependency(w, "evidence archive is not wired (no PG store)")
		return
	}
	hash := r.PathValue("content_hash")
	if !evidence.IsContentHash(hash) {
		writeError(w, fmt.Errorf("%w: content_hash must be 64 lowercase hex chars", serving.ErrInvalidParameter))
		return
	}
	rec, err := s.evidence.Get(r.Context(), hash)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// evidenceListResponse 是列表读面的报文形状。
type evidenceListResponse struct {
	Items  []evidence.Record `json:"items"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

// handleListEvidence 是 `GET /api/evidence?source=&dataset=&limit=&offset=`。
// limit 缺省 20、上限 100；越界一律 400，不静默截断。
func (s *Server) handleListEvidence(w http.ResponseWriter, r *http.Request) {
	if !s.dependencyReady(w) {
		return
	}
	if s.evidence == nil {
		writeDependency(w, "evidence archive is not wired (no PG store)")
		return
	}
	q := r.URL.Query()
	limit, err := parseBoundedInt(q.Get("limit"), "limit", defaultEvidenceLimit, 1, maxEvidenceLimit)
	if err != nil {
		writeError(w, err)
		return
	}
	offset, err := parseBoundedInt(q.Get("offset"), "offset", 0, 0, -1)
	if err != nil {
		writeError(w, err)
		return
	}
	items, err := s.evidence.List(r.Context(), store.EvidenceFilter{
		Source:  q.Get("source"),
		Dataset: q.Get("dataset"),
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []evidence.Record{}
	}
	writeJSON(w, http.StatusOK, evidenceListResponse{Items: items, Limit: limit, Offset: offset})
}

// parseBoundedInt 解析整数查询参数：空串取默认值；max < 0 表示只设下界。
func parseBoundedInt(raw, name string, def, min, max int) (int, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %s must be an integer", serving.ErrInvalidParameter, name)
	}
	if n < min {
		return 0, fmt.Errorf("%w: %s must be >= %d", serving.ErrInvalidParameter, name, min)
	}
	if max >= 0 && n > max {
		return 0, fmt.Errorf("%w: %s must be <= %d", serving.ErrInvalidParameter, name, max)
	}
	return n, nil
}
