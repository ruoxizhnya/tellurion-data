package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/ruoxizhnya/tellurion-kit/auth"
	kithttp "github.com/ruoxizhnya/tellurion-kit/auth/httpadapter"
)

// principalKey 是调用方身份在请求 context 里的键（运维审计用，例如
// `sync_schedules.created_by`）。
type principalKey struct{}

// withPrincipal 把放行后的身份挂进 context；无身份（开放访问）不动请求。
func withPrincipal(r *http.Request, principal string) *http.Request {
	if principal == "" {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, principal))
}

// subjectFromContext 取请求的调用方身份；未鉴权 / 开放访问返回空串。
func subjectFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(principalKey{}).(string); ok {
		return v
	}
	return ""
}

// 鉴权闸的失败分类。实现只负责把 kit 的判定映射到这三个哨兵。
var (
	ErrUnauthenticated = errors.New("no valid credentials")
	ErrForbidden       = errors.New("insufficient role")
	// ErrAuthUnavailable 是「没有 JWT_SECRET」的第 2 态：写面必须显式拒绝，
	// 不能退化成「鉴权没启用 = 已通过」。
	ErrAuthUnavailable = errors.New("authentication is not configured")
)

// Authorizer 是本仓所有端点的鉴权闸。nil = 闸未装配，端点必须拒绝服务。
//
// 放行时返回调用方身份（限流键）；无身份概念（开放访问）返回空串，
// 由 `rateKey` 归一到 `open-access`。身份只用于限流分桶，不参与鉴权判定
// ——鉴权结论仍由 Authorize 的 error 表达。
//
// 三态（六仓统一口径，2026-11-12）：
//  1. 有 secret ⇒ 全闸：写面必须带 token + 角色；
//  2. 无 secret 且未显式声明开放 ⇒ 读面公开、写面 503 auth_unavailable；
//  3. 无 secret 且显式 AUTH_INSECURE_EXPOSURE=loopback-published ⇒ 全放行
//     （`NewOpenAuthorizer`），启动日志必须 WARN 点名写面无鉴权。
type Authorizer interface {
	Authorize(r *http.Request) (principal string, err error)
}

// WithAuthorizer 装配端点鉴权闸。
func WithAuthorizer(a Authorizer) Option {
	return func(s *Server) { s.authorizer = a }
}

// NewAuthorizer 用 kit 的 net/http 适配构造鉴权闸：token 解析 / kind=access /
// 角色 any-of 都在 kit，本仓只提供策略表（policy.go）。
//
// TokenService 未启用（无 secret）时进入第 2 态：**读面公开、写面 503**，
// 不再等价于「鉴权没启用 = 已通过」。显式开放（第 3 态）用 `NewOpenAuthorizer`。
func NewAuthorizer(svc *auth.TokenService) Authorizer {
	return kitAuthorizer{svc: svc}
}

// NewOpenAuthorizer 是第 3 态的显式 allow-all 闸：无 secret，但运维已声明
// `AUTH_INSECURE_EXPOSURE=loopback-published`。调用方（cmd/dataservice）必须在
// 启动时 WARN 点名「当前写面无鉴权」；本类型不做任何判定。
func NewOpenAuthorizer() Authorizer { return openAuthorizer{} }

type openAuthorizer struct{}

func (openAuthorizer) Authorize(*http.Request) (string, error) { return "", nil }

type kitAuthorizer struct {
	svc *auth.TokenService
}

func (a kitAuthorizer) Authorize(r *http.Request) (string, error) {
	if a.svc == nil || !a.svc.Enabled() {
		return authorizeWithoutSecret(r)
	}
	key := kithttp.RouteKey(r)
	rule, declared := servedPolicy.Rule(key)
	if !declared {
		// 路由表 ≡ 策略表由守卫测试钉住；运行时仍 fail-closed。
		return "", fmt.Errorf("%w: no authorization policy for %q", ErrForbidden, key)
	}
	if rule.Public {
		return "", nil
	}
	claims, err := kithttp.Authenticate(a.svc, r)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if err := kithttp.Authorize(claims, rule); err != nil {
		if errors.Is(err, kithttp.ErrForbidden) {
			return "", fmt.Errorf("%w: %v", ErrForbidden, err)
		}
		return "", fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	return principalOf(claims), nil
}

// authorizeWithoutSecret 是第 2 态（无 secret、未声明开放）的判定：
//   - 公开路由（/healthz、/health 过渡别名、/readyz）放行；
//   - 读方法（GET / HEAD / OPTIONS / TRACE）保持公开；
//   - 变更方法一律 ErrAuthUnavailable ⇒ 503 auth_unavailable。
//
// 判定按「路由是否声明」fail-closed：未声明的路由仍拒绝，避免把漏登策略
// 误读成读面。
func authorizeWithoutSecret(r *http.Request) (string, error) {
	key := kithttp.RouteKey(r)
	if _, declared := servedPolicy.Rule(key); !declared {
		return "", fmt.Errorf("%w: no authorization policy for %q", ErrForbidden, key)
	}
	if kithttp.IsMutating(r.Method) {
		return "", fmt.Errorf("%w: no JWT secret configured; write surface is locked", ErrAuthUnavailable)
	}
	return "", nil
}

// principalOf 取限流键：username 优先、缺失退 subject（服务 token 只有 sub）。
func principalOf(claims *auth.Claims) string {
	if claims == nil {
		return ""
	}
	if claims.Username != "" {
		return claims.Username
	}
	return claims.Subject
}

// requireAuth 是 fail-closed 闸：未装配 Authorizer → 503；无 secret 的写面 →
// 503 auth_unavailable；无凭证 / 坏 token → 401；角色不足 → 403。只有 Authorize
// 返回 nil 才进入 handler。
//
// 顺序是**鉴权先于限流**（G1）：401 / 403 永远不会被 429 顶替。反过来的话，
// 未登录调用方会被读成「被限流」，core 侧 AUD-57 的同型坑（探针被限流读成
// 未登录，整站弹登录页）。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authorizer == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errPayload{
				Code:    "auth_unavailable",
				Message: "endpoints require an authorizer; none is configured",
			}})
			return
		}
		principal, err := s.authorizer.Authorize(r)
		switch {
		case err == nil:
			if s.limiter != nil && !s.limiter.allow(rateKey(principal), time.Now()) {
				w.Header().Set("Retry-After", "1")
				writeJSON(w, http.StatusTooManyRequests, errorBody{Error: errPayload{
					Code:    "rate_limited",
					Message: "request rate exceeded; retry after 1s",
				}})
				return
			}
			next.ServeHTTP(w, withPrincipal(r, principal))
		case errors.Is(err, ErrUnauthenticated):
			writeJSON(w, http.StatusUnauthorized, errorBody{Error: errPayload{Code: "unauthenticated", Message: "authentication required"}})
		case errors.Is(err, ErrForbidden):
			writeJSON(w, http.StatusForbidden, errorBody{Error: errPayload{Code: "forbidden", Message: "insufficient role"}})
		case errors.Is(err, ErrAuthUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: errPayload{
				Code:    "auth_unavailable",
				Message: "authentication is not configured; write endpoints are unavailable",
			}})
		default:
			log.Printf("httpapi: authorizer error: %v", err)
			writeJSON(w, http.StatusInternalServerError, errorBody{Error: errPayload{Code: "internal", Message: "internal error"}})
		}
	})
}
