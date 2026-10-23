# 0003 · connector 搬入本仓，传输以窄 port 留在 core

- **状态**：已裁决（X1c，2026-10-24）
- **场景**：12 个 connector 的**声明与归一化**必须在数据服务内（注册门要在这里执法），但其中 4 个 Tushare 门依赖 `tellurion:pkg/data.TushareClient`——那不是 connector 逻辑，是 HTTP 客户端 + 配额闸 + `ingest.raw` 归档，属**传输层**。本仓禁止 import core 的实现包（`AGENTS.md` §3），而工单禁区写着「不重写现有 connector 语义」。
- **决策**：
  1. connector 的取数编排、字段映射、声明全部搬入 `internal/ingest`；
  2. 传输抽成**本仓定义的窄接口**（`TushareTransport` / `MootdxTransport` / 东财 `EastmoneyClient`），实现留在 core，由 `ingest.Transports` 显式注入；nil = 该门 disabled（启动不失败，取数时跳过）；
  3. 传输层的错误分类在**装配边界**翻译成本仓哨兵（`ErrTransportInvalidInput` / `ErrTransportDataQuality`），判据仍是 code 不是文本。
- **理由**：搬运的目标是**边界**（谁声明、谁归一化、谁执法），不是把 HTTP 重写成第二份。把传输一起搬会同时踩两条线：既要重写（禁区），又要在本仓复制 raw 归档与配额闸（双写）。
- **代价 / 后果**：① 本仓单独 checkout 时 Tushare 系门是 disabled——注册与声明可测，取数不可跑；这是**如实的能力缺口**，不是「隐藏的 fallback」。② `Transports` 增加字段是向后兼容的（零值 nil）；删字段不行。③ core 侧 `pkg/data/source` 的旧实现要等跨仓切换才删，在此之前**同一个 connector 有两份代码**——已登记为未落项，且以本仓 `Declarations()` 为唯一声明真源，避免「两份声明各自漂移」。④ 传输整体迁入本仓时只需换实现，接口与调用点不动。
- **替代关系**：无（补充 0001 的协议边界，不取代它）