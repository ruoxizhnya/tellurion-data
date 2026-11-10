package syncjob

// 5 段 cron（分 时 日 月 周）的解析与下一次触发计算。
//
// 为什么自己写而不是引库：本仓只支持**标准 5 段**这一种形状，引一个库回来
// 还要用配置关掉它支持的第二/秒级语法（关错了就是「cron 写 6 段静默按 5 段
// 解释」）。60 行的解析器 + 逐条测试比这个风险小。
//
// 支持的语法：
//
//	*       任意值            ?       同 *（Quartz 习惯，库里历史数据常见）
//	5       单值              1-5     闭区间
//	*/15    步长              1-30/5  区间步长
//	1,3,5   列表（可与其他组合）
//
// 星期 0–7（0 与 7 都是周日）。日与周**同时受限**时取「或」（POSIX cron 口径）。

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronExpr 是解析后的位图（位 i = 取值 i）。
type cronExpr struct {
	minute  uint64
	hour    uint64
	dom     uint64
	month   uint64
	dow     uint64
	domStar bool
	dowStar bool
}

// NextRun 返回严格晚于 after 的下一次触发时刻（按 after 的时区计算）。
//
// 解析失败返回错误；5 年内没有匹配时刻也返回错误（例如 `0 0 30 2 *`）——
// 「永远不再触发」的排程必须响，而不是静默留在 is_active=true。
func NextRun(expression string, after time.Time) (time.Time, error) {
	expr, err := parseCron(expression)
	if err != nil {
		return time.Time{}, err
	}

	loc := after.Location()
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !hasBit(expr.month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
			continue
		}
		if !expr.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
			continue
		}
		if !hasBit(expr.hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc).Add(time.Hour)
			continue
		}
		if !hasBit(expr.minute, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cron %q never fires within 5 years of %s", expression, after.Format(time.RFC3339))
}

// dayMatches 实现日/周的「同时受限取或」语义。
func (c cronExpr) dayMatches(t time.Time) bool {
	byDOM := hasBit(c.dom, t.Day())
	byDOW := hasBit(c.dow, int(t.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return byDOW
	case c.dowStar:
		return byDOM
	default:
		return byDOM || byDOW
	}
}

// parseCron 解析 5 段表达式。
func parseCron(expression string) (cronExpr, error) {
	fields := strings.Fields(strings.TrimSpace(expression))
	if len(fields) != 5 {
		return cronExpr{}, fmt.Errorf("need exactly 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}
	var out cronExpr
	var err error
	if out.minute, _, err = parseCronField(fields[0], 0, 59); err != nil {
		return cronExpr{}, fmt.Errorf("minute: %w", err)
	}
	if out.hour, _, err = parseCronField(fields[1], 0, 23); err != nil {
		return cronExpr{}, fmt.Errorf("hour: %w", err)
	}
	if out.dom, out.domStar, err = parseCronField(fields[2], 1, 31); err != nil {
		return cronExpr{}, fmt.Errorf("day-of-month: %w", err)
	}
	if out.month, _, err = parseCronField(fields[3], 1, 12); err != nil {
		return cronExpr{}, fmt.Errorf("month: %w", err)
	}
	// 星期允许 7 = 周日：解析后再把 bit7 折到 bit0。
	if out.dow, out.dowStar, err = parseCronField(fields[4], 0, 7); err != nil {
		return cronExpr{}, fmt.Errorf("day-of-week: %w", err)
	}
	if hasBit(out.dow, 7) {
		out.dow |= 1
	}
	return out, nil
}

// parseCronField 把一个字段解析成位图，并报告它是否是「全通配」。
func parseCronField(field string, min, max int) (uint64, bool, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return 0, false, fmt.Errorf("empty field")
	}
	if field == "*" || field == "?" {
		return rangeMask(min, max), true, nil
	}

	var mask uint64
	star := false
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, false, fmt.Errorf("empty list element in %q", field)
		}
		// 步长：base/step
		base, step := part, 1
		hasStep := false
		if idx := strings.Index(part, "/"); idx >= 0 {
			base = strings.TrimSpace(part[:idx])
			rawStep := strings.TrimSpace(part[idx+1:])
			n, err := strconv.Atoi(rawStep)
			if err != nil || n <= 0 {
				return 0, false, fmt.Errorf("step %q in %q must be a positive integer", rawStep, part)
			}
			step = n
			hasStep = true
		}

		lo, hi := min, max
		switch {
		case base == "*":
			star = true
		case strings.Contains(base, "-"):
			pieces := strings.SplitN(base, "-", 2)
			a, err := strconv.Atoi(strings.TrimSpace(pieces[0]))
			if err != nil {
				return 0, false, fmt.Errorf("range start %q is not an integer", pieces[0])
			}
			b, err := strconv.Atoi(strings.TrimSpace(pieces[1]))
			if err != nil {
				return 0, false, fmt.Errorf("range end %q is not an integer", pieces[1])
			}
			if a < min || b > max || a > b {
				return 0, false, fmt.Errorf("range %d-%d outside [%d,%d]", a, b, min, max)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(base)
			if err != nil {
				return 0, false, fmt.Errorf("%q is not an integer", base)
			}
			if v < min || v > max {
				return 0, false, fmt.Errorf("value %d outside [%d,%d]", v, min, max)
			}
			// 单值只在**带步长**时展开到上界（`5/15` ≡ `5-<max>/15`）；
			// 裸单值 `9` 就是 9，展开成 9–23 会让 `0 9 * * 1-5` 在 18 点触发。
			lo = v
			hi = v
			if hasStep {
				hi = max
			}
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, star, nil
}

func rangeMask(min, max int) uint64 {
	var mask uint64
	for v := min; v <= max; v++ {
		mask |= 1 << uint(v)
	}
	return mask
}

func hasBit(mask uint64, v int) bool {
	if v < 0 || v > 63 {
		return false
	}
	return mask&(1<<uint(v)) != 0
}
