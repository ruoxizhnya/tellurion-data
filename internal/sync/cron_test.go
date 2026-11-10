package syncjob

import (
	"strings"
	"testing"
	"time"
)

func TestNextRun_Step(t *testing.T) {
	after := time.Date(2026, 11, 10, 10, 7, 0, 0, time.UTC)
	next, err := NextRun("*/15 * * * *", after)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if got, want := next.Format("2006-01-02 15:04"), "2026-11-10 10:15"; got != want {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextRun_WeekdaysOnlySkipsWeekend(t *testing.T) {
	// 2026-11-13 是周五。
	friday := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	next, err := NextRun("0 9 * * 1-5", friday)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if got, want := next.Format("2006-01-02 15:04"), "2026-11-16 09:00"; got != want {
		t.Fatalf("next = %s, want %s (Monday)", got, want)
	}
}

func TestNextRun_MonthBoundary(t *testing.T) {
	after := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	next, err := NextRun("0 0 1 * *", after)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if got, want := next.Format("2006-01-02 15:04"), "2026-12-01 00:00"; got != want {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextRun_LeapDay(t *testing.T) {
	after := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	next, err := NextRun("0 0 29 2 *", after)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if got, want := next.Format("2006-01-02"), "2028-02-29"; got != want {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextRun_SundayAsSeven(t *testing.T) {
	// 2026-11-10 是周二。
	after := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	next, err := NextRun("30 8 * * 7", after)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if got, want := next.Weekday().String(), "Sunday"; got != want {
		t.Fatalf("weekday = %s, want %s", got, want)
	}
	if got, want := next.Format("15:04"), "08:30"; got != want {
		t.Fatalf("time = %s, want %s", got, want)
	}
}

func TestNextRun_DomAndDowAreOred(t *testing.T) {
	// 13 号或周五：2026-11-11（周三）之后第一个是 11-13（周五）。
	after := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	next, err := NextRun("0 0 13 * 5", after)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if got, want := next.Format("2006-01-02"), "2026-11-13"; got != want {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextRun_AcceptsQuartzQuestionMark(t *testing.T) {
	after := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	if _, err := NextRun("0 3 ? * ?", after); err != nil {
		t.Fatalf("NextRun with ?: %v", err)
	}
}

func TestNextRun_RejectsBadExpressions(t *testing.T) {
	cases := []string{
		"",
		"* * * *",
		"* * * * * *",
		"*/0 * * * *",
		"61 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"5-1 * * * *",
		"a * * * *",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			if _, err := NextRun(expr, time.Now()); err == nil {
				t.Fatalf("NextRun(%q) = nil error, want parse failure", expr)
			}
		})
	}
}

func TestNextRun_NeverFiresReturnsError(t *testing.T) {
	// 2 月 30 日不存在：必须报「永远不会触发」，而不是返回零值或卡死。
	_, err := NextRun("0 0 30 2 *", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("want error for impossible schedule")
	}
	if !strings.Contains(err.Error(), "never fires") {
		t.Fatalf("err = %v, want never-fires message", err)
	}
}
