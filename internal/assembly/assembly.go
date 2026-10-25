// Package assembly 是写面的**装配层** —— ADR-0004 第 2 条里那句「投影在
// 未来的装配层完成」的那个装配层。
//
// 它存在的理由是包边纪律：`internal/ingest` 不许 import `internal/store`，
// `store` 也不许 import `ingest`（存储不认识采集语义），于是
// `ingest.UnifiedDataPoint → store.Point` 的投影必须有人持有。本包同时
// import 两者，别处不再各抄一份投影。
//
// 两条入口共用这一条链，这不是偷懒而是刻意：
//   - `POST /ingest/points`（`internal/httpapi`）→ `Writer.Write`
//   - 作业层（传输迁入本仓之后）→ `Pipeline`
//
// 分开放两套的话，「跳过 / 去重 / 闸门」的计数口径一定会漂移，而漂移的那一份
// 看起来都是对的。
//
// 可见时刻与入库时刻都**不接受投送**：前者只由该 data_type 声明的锚点推导
// （`ingest.GatePoints`），后者只由本进程注入的时钟填。调用方递来的
// `visible_at` 会让 `store` 那道「整批拒收」的门变成装饰——闸门拦不住一个
// 自己声明「我可见」的批次。
package assembly

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"tellurion-data/internal/ingest"
	"tellurion-data/internal/normalize"
	"tellurion-data/internal/store"
)

// MaxPointsPerRequest 是单次写入的点位上界。
//
// 这条不是性能参数，是 `docs/dataflow.md` §2 那条红线的落地：「同步 HTTP 端点
// 不得承载无界批量——无界批量一律走作业层」。没有上界的 HTTP 灌数入口就是把
// 作业层的形状（一次事务吞下整池）伪装成一个请求。超限**整批拒收**并点名上界，
// 调用方按上界分片，而不是本包悄悄截断——截断 = 库里留一个没人知道的洞。
const MaxPointsPerRequest = 10000

// 装配面哨兵。哨兵只增不删不改语义（`docs/api-schema.md` §2 三条纪律）。
var (
	// ErrMisconfigured 是装配错误（nil registry / nil sink）。启动期 bug，
	// 与「数据被拒收」必须分开：前者要改装配代码，后者要改上游数据。
	ErrMisconfigured = errors.New("assembly: write面 misconfigured")
	// ErrInvalidBatch 是批次本身不合法：没给 data_type、空批次、点上的
	// data_type 与批次不一致、来源空白。一条哨兵，报文点名是哪一种——
	// 这四种都是「同一个处置动作」（调用方修请求），拆成四条哨兵就是
	// api-schema §2 禁的那种同义两义。
	ErrInvalidBatch = errors.New("assembly: invalid point batch")
	// ErrBatchTooLarge 是超过 `MaxPointsPerRequest`。它与 ErrInvalidBatch
	// 分开是因为处置不同：那条改内容，这条改分片。
	ErrBatchTooLarge = errors.New("assembly: batch exceeds the per-request point limit")
)

// Point 是一个可投送的点位。字段是 `ingest.UnifiedDataPoint` 的**去掉两个
// 服务器独占字段**的子集：`IngestTime` 由本进程时钟填，`VisibleAt` 由声明
// 锚点推导，两者都不在投送面上。
type Point struct {
	Symbol    string         `json:"symbol"`
	TradeTime time.Time      `json:"trade_time"`
	Source    string         `json:"source"`
	DataType  string         `json:"data_type,omitempty"`
	Data      map[string]any `json:"data"`
}

// Request 是一次写入请求。
type Request struct {
	DataType string  `json:"data_type"`
	Points   []Point `json:"points"`
}

// Result 是一次写入的计数。口径与写面一致：
//   - `persisted` = 请求点已就位（新插入 + 同内容 no-op），不是新增行数；
//   - `skipped`   = 形态不合法被丢的点 + 幂等坐标重复的点。
type Result struct {
	DataType  string `json:"data_type"`
	Received  int    `json:"received"`
	Persisted int    `json:"persisted"`
	Skipped   int    `json:"skipped"`
}

// PointWriter 是写面 sink。`*store.Writer` 直接满足；抽出来只为让装配链能
// 被无库测试（假的 sink 记录它收到什么）。
type PointWriter interface {
	BulkInsert(ctx context.Context, dataType string, points []store.Point) (persisted, skipped int, err error)
}

// Writer 是 HTTP 投点入口用的写面装配。
type Writer struct {
	reg  *ingest.Registry
	sink PointWriter
	now  func() time.Time
}

// New 构造装配。clock 为 nil 用墙钟（与 `ingest.NewETLPipeline` 同口径）；
// 测试注入固定时钟，于是「同输入同字节」在写侧也成立。
func New(reg *ingest.Registry, sink PointWriter, clock func() time.Time) (*Writer, error) {
	if reg == nil {
		return nil, fmt.Errorf("%w: nil registry", ErrMisconfigured)
	}
	if sink == nil {
		return nil, fmt.Errorf("%w: nil point writer", ErrMisconfigured)
	}
	if clock == nil {
		clock = time.Now
	}
	return &Writer{reg: reg, sink: sink, now: clock}, nil
}

// DeclarationsRegistry 建一个**只持声明**的注册表：12 个 connector 全部以
// 「传输 = nil（该门 disabled）」登记，于是 `DeclarationFor` 可用而 `Fetch`
// 不可用。
//
// 写端点只需要声明（闸门要锚点列名），不需要取数——传输实现仍在 core
// （`internal/ingest/transport.go` 头注释 + AGENTS.md §3），本仓不许自己造。
func DeclarationsRegistry() (*ingest.Registry, error) {
	reg := ingest.NewRegistry()
	if err := ingest.RegisterConnectors(reg, ingest.Transports{}); err != nil {
		return nil, err
	}
	return reg, nil
}

// Write 执行一次投点写入。步骤与 `ingest.ETLPipeline.Process` 的取数之后
// 那半逐字同序（声明门 → 归一 → 校验 → 闸门 → 去重 → 写面），任何一步
// 失败都是**整批拒收且不触达 store**。
func (w *Writer) Write(ctx context.Context, req Request) (Result, error) {
	dataType := strings.TrimSpace(req.DataType)
	if dataType == "" {
		return Result{}, fmt.Errorf("%w: data_type is required", ErrInvalidBatch)
	}
	if len(req.Points) == 0 {
		return Result{}, fmt.Errorf("%w: %s has no points", ErrInvalidBatch, dataType)
	}
	if len(req.Points) > MaxPointsPerRequest {
		return Result{}, fmt.Errorf("%w: %s carries %d points, above the per-request limit of %d — shard the batch (unbounded batches belong to the job layer, docs/dataflow.md §2)",
			ErrBatchTooLarge, dataType, len(req.Points), MaxPointsPerRequest)
	}

	// 声明门在取任何数、写任何行之前：没登记过可见语义的 data_type 不该
	// 被「先收下来再说」。
	decl, err := w.reg.DeclarationFor(dataType)
	if err != nil {
		return Result{}, err
	}

	result := Result{DataType: dataType, Received: len(req.Points)}
	ingestTime := w.now().UTC()
	points := make([]ingest.UnifiedDataPoint, 0, len(req.Points))
	for i, p := range req.Points {
		if p.DataType != "" && p.DataType != dataType {
			return Result{}, fmt.Errorf("%w: point[%d] data_type %q != %q", ErrInvalidBatch, i, p.DataType, dataType)
		}
		if strings.TrimSpace(p.Source) == "" {
			// 来源是行内容的一部分（换源 = 内容变了，写面会响亮拒收），
			// 空白来源等于放弃那条判定。
			return Result{}, fmt.Errorf("%w: point[%d] %s has no source", ErrInvalidBatch, i, p.Symbol)
		}
		points = append(points, ingest.NewUnifiedDataPoint(p.Symbol, p.Source, dataType, p.TradeTime, ingestTime, p.Data))
	}

	valid, invalid := ingest.ValidatePoints(points)
	result.Skipped += invalid

	gated, err := ingest.GatePoints(decl, valid)
	if err != nil {
		// GatePoints 失败时返回 nil 切片；这里仍然显式不触达 store，
		// 让「整批拒收」在读侧可证明，而不是靠下游碰巧没写。
		return Result{}, err
	}

	deduped, dup := ingest.DeduplicateWithCount(gated)
	result.Skipped += dup
	if len(deduped) == 0 {
		return result, nil
	}

	persisted, storeSkipped, err := w.sink.BulkInsert(ctx, dataType, ProjectAll(deduped))
	if err != nil {
		return Result{}, err
	}
	result.Persisted = persisted
	result.Skipped += storeSkipped
	return result, nil
}

// Project 把一个采集点位投影成写面点位（七个字段逐字搬，不做任何解释）。
//
// 字段顺序不进这里——投影必须恒等，任何「顺手补个默认值」都会让
// `store` 的内容比对对着一个被改过的行判定「换源了」。
func Project(p ingest.UnifiedDataPoint) store.Point {
	return store.Point{
		Symbol:     p.Symbol,
		TradeTime:  p.TradeTime,
		Source:     p.Source,
		DataType:   p.DataType,
		Data:       p.Data,
		IngestTime: p.IngestTime,
		VisibleAt:  p.VisibleAt,
	}
}

// ProjectAll 是 `Project` 的切片形态；nil/空切片返回空切片而不是 nil，
// 让调用方的「没东西可写」与「投影坏了」在计数上可分辨。
func ProjectAll(points []ingest.UnifiedDataPoint) []store.Point {
	out := make([]store.Point, 0, len(points))
	for _, p := range points {
		out = append(out, Project(p))
	}
	return out
}

// Inserter 把写面 sink 适配成 `ingest.BulkInserter`（作业侧那条链的注入点）。
type Inserter struct{ sink PointWriter }

// NewInserter 返回实现 `ingest.BulkInserter` 的适配器。
func NewInserter(sink PointWriter) ingest.BulkInserter { return Inserter{sink: sink} }

// BulkInsert 投影后转给写面。
func (i Inserter) BulkInsert(ctx context.Context, dataType string, points []ingest.UnifiedDataPoint) (int, int, error) {
	return i.sink.BulkInsert(ctx, dataType, ProjectAll(points))
}

// Pipeline 装出一条完整的作业链（取数 → 归一 → 校验 → 闸门 → 去重 → 写面）。
//
// 它今天没有生产调用方：传输仍在 core。落它不是为了「先占个位」，而是让
// HTTP 投点这条入口与作业那条入口**共用同一个写侧次序**这件事成为可测的
// 事实（见 `TestWriteAndPipelineAgreeOnTheSameBatch`）。
func Pipeline(reg *ingest.Registry, sink PointWriter, clock func() time.Time) *ingest.ETLPipeline {
	pipeline := ingest.NewETLPipeline(reg, NewInserter(sink))
	if clock != nil {
		pipeline = pipeline.WithClock(clock)
	}
	return pipeline
}

// WireError 把写面失败分诊成 HTTP 状态码与错误码，供 `internal/httpapi` 查表。
//
// 映射放本包而不是 httpapi，是因为这些哨兵来自本包 import 的三层
// （assembly / ingest / store）；让 httpapi 直接 import 那三个包换一张表，
// 等于把接口层变成唯一知道写侧内部结构的地方。未登记的错误 ok=false，
// 由调用方按 500 处理——不许把未知失败伪装成 4xx。
func WireError(err error) (status int, code string, ok bool) {
	switch {
	// `store` 的「缺可见时刻」与 `normalize` / `ingest` 的是同一个哨兵
	// （`store.ErrVisibleAtMissing = normalize.ErrVisibleAtMissing`），所以
	// 三条路径上的整批拒收在这里只登记一条——两条同义哨兵正是
	// `api-schema.md` §2 禁的那种「按名字分诊分出错」。
	case errors.Is(err, ErrBatchTooLarge):
		return http.StatusRequestEntityTooLarge, "batch_too_large", true
	case errors.Is(err, ErrInvalidBatch):
		return http.StatusBadRequest, "invalid_batch", true
	case errors.Is(err, normalize.ErrVisibleAtMissing):
		return http.StatusBadRequest, "visible_at_missing", true
	case errors.Is(err, ingest.ErrAnchorInvalid):
		return http.StatusBadRequest, "anchor_invalid", true
	case errors.Is(err, ingest.ErrDeclarationMissing):
		return http.StatusBadRequest, "data_type_undeclared", true
	case errors.Is(err, ingest.ErrDeclarationInvalid), errors.Is(err, ingest.ErrDeclarationMismatch):
		return http.StatusInternalServerError, "declaration_invalid", true
	case errors.Is(err, store.ErrWriteTableNotInASet):
		return http.StatusBadRequest, "write_table_not_in_a_set", true
	case errors.Is(err, store.ErrUnmappedDataType):
		return http.StatusBadRequest, "data_type_unmapped", true
	case errors.Is(err, store.ErrInvalidPoint):
		return http.StatusBadRequest, "invalid_point", true
	case errors.Is(err, store.ErrContentConflict):
		return http.StatusConflict, "content_conflict", true
	default:
		return 0, "", false
	}
}
