package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
)

// G4 ①：只有连接层失败折成 ErrDependencyUnavailable；SQL 语义错误必须原样
// 上抛（否则「库挂了」会掩盖「我们发了一条坏 SQL」）。
func TestClassifyDependency(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"pg connection_exception", &pgconn.PgError{Code: "08006"}, true},
		{"pg admin shutdown", &pgconn.PgError{Code: "57P01"}, true},
		{"pg statement timeout", &pgconn.PgError{Code: "57014"}, true},
		{"pg undefined_table", &pgconn.PgError{Code: "42P01"}, false},
		{"dial refused", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, true},
		{"statement timeout", context.DeadlineExceeded, true},
		{"server hung up", io.EOF, true},
		{"plain sql bug", errors.New("syntax error at or near \"selct\""), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDependency(tc.err)
			if tc.want {
				assert.ErrorIs(t, got, ErrDependencyUnavailable)
				assert.NotErrorIs(t, tc.err, ErrDependencyUnavailable)
			} else {
				assert.False(t, errors.Is(got, ErrDependencyUnavailable), got)
			}
		})
	}
}

// 已经分诊过的错误不重复包裹：错误链上只出现一次哨兵，日志不套娃。
func TestClassifyDependencyIsIdempotent(t *testing.T) {
	once := classifyDependency(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")})
	twice := classifyDependency(once)
	assert.Equal(t, once.Error(), twice.Error())
	assert.ErrorIs(t, twice, ErrDependencyUnavailable)
	assert.NotContains(t, fmt.Sprint(twice), "store: postgres is unavailable: store: postgres is unavailable")
}
