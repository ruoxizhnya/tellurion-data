// Package ingest 是「采集 Ingestion」限界上下文（`docs/ARCHITECTURE.md` §2.1）。
//
// 本包只承载 **connector 注册契约**：每个数据源在注册时必须显式声明
// 「什么时候才可见」（`market.TimeSemantics`）与「什么节奏产出」
// （`market.FrequencySpec`），未声明 / 声明非法 / 声明与 wire schema 矛盾者
// 一律 fail-closed 拒收（`ErrDeclaration*` 三兄弟）。
//
// 取数传输（HTTP/SDK 客户端）仍在 `tellurion:pkg/data/source`，经 `Transports`
// 窄接口注入；本包承载注册门、兜底取数编排（`Registry.Fetch`）、ETL 管线
// （`ETLPipeline`）、写侧可见性闸门（`GatePoints`）与去重（`Deduplicate`）。
package ingest

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
)

// VisibleFieldTradeTime 是保留锚点记号：可见时间取自 `DataItem.TradeTime`
// （bar 时间 / 快照时刻），而不是某个 wire 字段。
const VisibleFieldTradeTime = "trade_time"

// VisibleFieldAnnDate 是保留锚点记号：可见时间取自规范层
// `market.Fundamental.AnnDate`（披露日）。财报 connector 的 TradeTime 承载
// 报告期截止日，拿它当锚点等于把前视洞放回来。
const VisibleFieldAnnDate = "ann_date"

// ConnectorDeclaration 是单个数据类型的注册声明。
//
// 字段与 `tellurion:pkg/data/source/contract.go` 的 `ConnectorDeclaration`
// 逐字对齐（X1a 后两者合并进 contracts 的 `dataprotocol`；在那之前本包是
// 数据服务侧的接收面）。新增字段必须可选，否则所有 connector 编译期红。
type ConnectorDeclaration struct {
	// DataType 必须是 Connector.SupportedTypes() 的成员。
	DataType string
	// TimeSemantics 时间语义（何时可见）。必填且必须是合法枚举。
	TimeSemantics market.TimeSemantics
	// VisibleExpr 自定义时点的判定表达；TimeSemantics == VisibleCustom 时必填。
	VisibleExpr string
	// VisibleField 记录中承载可见时间锚点的字段名：wire schema 里的
	// date/datetime 字段，或保留记号（trade_time / ann_date）。必填。
	VisibleField string
	// Frequency 频率口径。必填且必须是合法枚举。
	Frequency market.FrequencySpec
}

// SchemaField 是 wire schema 的单列。Type 取值集与 core 一致：
// "string" | "float" | "int" | "date" | "datetime"。
//
// Required / Unit 是 X1c 搬 connector 时补上的两个字段（core 同形）：
// 注册门只关心 Name/Type（锚点校验），Required/Unit 供 ETL 与运维读，
// 参与「缺失≠0」与单位口径的声明。
type SchemaField struct {
	Name     string
	Type     string
	Required bool
	Unit     string // "yuan" | "percent" | "share" | ""
}

// DataSchema 声明某个数据类型在 wire 上的字段形状。
type DataSchema struct {
	DataType string
	Fields   []SchemaField
}

// Connector 是注册期需要的 connector 面。
//
// 刻意比 core 的 `DataSourceAdapter` 窄：本仓今天没有取数传输实现，
// Fetch / HealthCheck / RateLimit 随 X1c 一并与 connector 迁入。窄接口让
// 「注册门」可以被独立测试，而不是必须先造一个 9 方法假实现。
type Connector interface {
	// Name 是稳定标识（如 "tushare" / "mootdx"），入 catalog 与日志。
	Name() string
	// SupportedTypes 是该 connector 服务的数据类型**字面值**列表。
	SupportedTypes() []string
	// Declarations 必须覆盖 SupportedTypes 的每个成员，且不多不少。
	Declarations() []ConnectorDeclaration
	// Schema 返回某数据类型的 wire schema。锚点校验用它；不可得时返回错误。
	Schema(dataType string) (DataSchema, error)
}

// fail-closed 拒收的三类哨兵。errors.Is 分诊：
// missing = 漏声明；invalid = 声明本身非法；mismatch = 声明与数据形态矛盾。
var (
	ErrDeclarationMissing  = errors.New("ingest: connector declaration missing")
	ErrDeclarationInvalid  = errors.New("ingest: connector declaration invalid")
	ErrDeclarationMismatch = errors.New("ingest: connector declaration contradicts data schema")
	// ErrAlreadyRegistered 是重复注册同名 connector（后者不覆盖前者）。
	ErrAlreadyRegistered = errors.New("ingest: connector already registered")
	// ErrNotRegistered 是查询未登记的名字。
	ErrNotRegistered = errors.New("ingest: connector not registered")
)

// Registry 是 connector 注册表。并发安全。
//
// chains 是「每个 data_type 的兜底链」（按登记顺序，先登记的优先）。
// 它由 `Register` 自动维护，不用手抄一份——手抄的链迟早与 SupportedTypes
// 漂移，而漂移后果是「链里有一个从不服务该类型的源」，取数时才炸。
type Registry struct {
	mu         sync.RWMutex
	connectors map[string]Connector
	chains     map[string][]string
}

// NewRegistry 返回空注册表。
func NewRegistry() *Registry {
	return &Registry{
		connectors: make(map[string]Connector),
		chains:     make(map[string][]string),
	}
}

// Register 校验并登记 connector，通过返回 nil。
//
// 校验覆盖面（与 core `validateDeclarations` 同构，只增不减）：
// 每个 SupportedTypes 成员有且仅有一条声明、声明不含多余类型、时间语义 /
// 频率口径枚举合法、自定义语义附判定表达、可见锚点可从 schema 推导。
func (r *Registry) Register(c Connector) error {
	if c == nil {
		return fmt.Errorf("%w: nil connector", ErrDeclarationInvalid)
	}
	name := strings.TrimSpace(c.Name())
	if name == "" {
		return fmt.Errorf("%w: connector name is empty", ErrDeclarationInvalid)
	}
	if err := validateDeclarations(c); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.connectors[name]; dup {
		return fmt.Errorf("%w: %s", ErrAlreadyRegistered, name)
	}
	r.connectors[name] = c
	// 兜底链按登记顺序追加（先登记 = 先尝试）。追加去重：同一 connector
	// 重复声明同一类型会被注册门拒掉，这里的去重只防 SetChain 之后的登记。
	for _, dt := range c.SupportedTypes() {
		r.chains[dt] = appendUnique(r.chains[dt], name)
	}
	return nil
}

// SetChain 覆盖某 data_type 的兜底链（运维显式配置 / 测试注入）。
// names 里的未知名字不在这里拒——取数时按 ErrAdapterNotRegistered 报错，
// 让 misconfig 在作业时可见，而不是注册期静默吞掉。
func (r *Registry) SetChain(dataType string, names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(names))
	copy(cp, names)
	r.chains[dataType] = cp
}

// Chains 返回兜底链的快照（拷贝），供装配日志与测试对账。
func (r *Registry) Chains() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]string, len(r.chains))
	for dt, names := range r.chains {
		cp := make([]string, len(names))
		copy(cp, names)
		out[dt] = cp
	}
	return out
}

func appendUnique(xs []string, s string) []string {
	if containsString(xs, s) {
		return xs
	}
	return append(xs, s)
}

// Get 按名字取 connector。
func (r *Registry) Get(name string) (Connector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.connectors[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotRegistered, name)
	}
	return c, nil
}

// Names 返回已登记名字（排序后的拷贝）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.connectors))
	for name := range r.connectors {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// validateDeclarations 是注册门本体。
func validateDeclarations(c Connector) error {
	decls := c.Declarations()
	supported := c.SupportedTypes()

	byType := make(map[string]ConnectorDeclaration, len(decls))
	for _, d := range decls {
		if _, dup := byType[d.DataType]; dup {
			return fmt.Errorf("%w: %s declares data type %q twice", ErrDeclarationInvalid, c.Name(), d.DataType)
		}
		byType[d.DataType] = d
	}

	for _, dt := range supported {
		d, ok := byType[dt]
		if !ok {
			return fmt.Errorf("%w: %s supports %q but has no declaration (missing time semantics + frequency spec)",
				ErrDeclarationMissing, c.Name(), dt)
		}
		if err := validateDeclaration(c, d); err != nil {
			return err
		}
	}

	for _, d := range decls {
		if !containsString(supported, d.DataType) {
			return fmt.Errorf("%w: %s declares data type %q which is not in SupportedTypes()",
				ErrDeclarationInvalid, c.Name(), d.DataType)
		}
	}
	return nil
}

// validateDeclaration 校验单条声明。
func validateDeclaration(c Connector, d ConnectorDeclaration) error {
	if !d.TimeSemantics.Valid() {
		return fmt.Errorf("%w: %s/%s time semantics %q is not a known value (known: %v)",
			ErrDeclarationInvalid, c.Name(), d.DataType, d.TimeSemantics, market.KnownTimeSemantics())
	}
	if d.TimeSemantics.RequiresExpression() && strings.TrimSpace(d.VisibleExpr) == "" {
		return fmt.Errorf("%w: %s/%s time semantics %q requires a decision expression (VisibleExpr)",
			ErrDeclarationInvalid, c.Name(), d.DataType, d.TimeSemantics)
	}
	if !d.Frequency.Valid() {
		return fmt.Errorf("%w: %s/%s frequency spec %q is not a known value (known: %v)",
			ErrDeclarationInvalid, c.Name(), d.DataType, d.Frequency, market.KnownFrequencySpecs())
	}
	return validateVisibleAnchor(c, d)
}

// validateVisibleAnchor 校验「声明与数据形态矛盾」：声明的可见时间锚点必须
// 能从该数据类型的 schema 推导，否则声明形同虚设。
func validateVisibleAnchor(c Connector, d ConnectorDeclaration) error {
	field := strings.TrimSpace(d.VisibleField)
	if field == "" {
		return fmt.Errorf("%w: %s/%s declares time semantics %q but names no visible anchor field (VisibleField)",
			ErrDeclarationMismatch, c.Name(), d.DataType, d.TimeSemantics)
	}
	// 保留锚点：语义由规范层承载，不要求出现在 wire schema。
	if field == VisibleFieldTradeTime || field == VisibleFieldAnnDate {
		return validateReservedAnchorProjection(c, d, field)
	}
	schema, err := c.Schema(d.DataType)
	if err != nil {
		return fmt.Errorf("%w: %s/%s schema unavailable for anchor check: %v",
			ErrDeclarationMismatch, c.Name(), d.DataType, err)
	}
	for _, f := range schema.Fields {
		if f.Name != field {
			continue
		}
		if f.Type != "date" && f.Type != "datetime" {
			return fmt.Errorf("%w: %s/%s visible anchor %q has type %q (want date/datetime)",
				ErrDeclarationMismatch, c.Name(), d.DataType, field, f.Type)
		}
		return nil
	}
	return fmt.Errorf("%w: %s/%s visible anchor %q not found in schema (fields: %v)",
		ErrDeclarationMismatch, c.Name(), d.DataType, field, schemaFieldNames(schema))
}

// validateReservedAnchorProjection 校验保留锚点的**可选 wire 投影**：
// 可以没有（语义仍在规范层），一旦有就必须是 date/datetime。
func validateReservedAnchorProjection(c Connector, d ConnectorDeclaration, field string) error {
	schema, err := c.Schema(d.DataType)
	if err != nil {
		return nil
	}
	for _, f := range schema.Fields {
		if f.Name != field {
			continue
		}
		if f.Type != "date" && f.Type != "datetime" {
			return fmt.Errorf("%w: %s/%s reserved anchor %q is projected with type %q (want date/datetime)",
				ErrDeclarationMismatch, c.Name(), d.DataType, field, f.Type)
		}
		return nil
	}
	return nil
}

func schemaFieldNames(s DataSchema) []string {
	out := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		out = append(out, f.Name)
	}
	return out
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
