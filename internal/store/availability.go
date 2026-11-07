package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrDependencyUnavailable 是「PG 不可达」这一类的唯一哨兵。
//
// 它与「SQL 错了」「数据被拒了」必须分开：依赖不可用是**可等待恢复**的
// （重连后同一请求往往能成功），而 4xx 与 5xx 的处置不同。HTTP 面上它
// 映射成 503 `dependency_unavailable`（`docs/api-schema.md` §2/§2.1）。
//
// 只包**连接层**失败，不包 SQL 语义错误：后者应当仍然是 500，否则
// 「库挂了」会被用来掩盖「我们发了一条坏 SQL」。
var ErrDependencyUnavailable = errors.New("store: postgres is unavailable")

// classifyDependency 把 pgx / 网络层的连接失败折成 ErrDependencyUnavailable。
//
// 判据（逐条都能从 pgx v5.7.3 的类型面读到）：
//   - `*pgconn.PgError` 且 SQLSTATE 是 08 类（connection_exception）；
//   - `*pgconn.ConnectError`（拨号/握手失败）；
//   - `pgconn.Timeout` / `context.DeadlineExceeded`（连接或语句超时）；
//   - `net.Error`（connection refused / reset / no route）；
//   - `io.EOF` / `io.ErrUnexpectedEOF`（服务端在查询中途断开）。
//
// 其它错误原样返回：不做「猜一个 503」的兜底。
func classifyDependency(err error) error {
	if err == nil || errors.Is(err, ErrDependencyUnavailable) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "08") ||
		// 57P01 admin_shutdown / 57P02 crash_shutdown / 57P03 cannot_connect_now：
		// 服务端主动不可用；57014 query_canceled 是本仓 statement_timeout(30s)
		// 到点的形态。它们是「现在不行」，不是「SQL 写错了」。
		strings.HasPrefix(pgErr.Code, "57P") || pgErr.Code == "57014") {
		return dependencyError(err)
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return dependencyError(err)
	}
	if pgconn.Timeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return dependencyError(err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return dependencyError(err)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return dependencyError(err)
	}
	return err
}

func dependencyError(err error) error {
	return fmt.Errorf("%w: %v", ErrDependencyUnavailable, err)
}
