package evidence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCanonicalHashIgnoresKeyOrder(t *testing.T) {
	a := json.RawMessage(`{"b":2,"a":1,"nested":{"y":"x","z":null}}`)
	b := json.RawMessage(`{"nested":{"z":null,"y":"x"},"a":1,"b":2}`)

	ha, err := CanonicalHash(a)
	if err != nil {
		t.Fatalf("CanonicalHash(a): %v", err)
	}
	hb, err := CanonicalHash(b)
	if err != nil {
		t.Fatalf("CanonicalHash(b): %v", err)
	}
	if ha != hb {
		t.Fatalf("同内容不同键序应得同一哈希：%s != %s", ha, hb)
	}
	if !IsContentHash(ha) {
		t.Fatalf("哈希应为 64 位小写 hex：%q", ha)
	}
}

func TestCanonicalHashKeepsNumberPrecision(t *testing.T) {
	a := json.RawMessage(`{"v":1.0000000000000002}`)
	b := json.RawMessage(`{"v":1}`)
	ha, err := CanonicalHash(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := CanonicalHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha == hb {
		t.Fatal("UseNumber 未生效：不同精度数值被折成同一哈希")
	}
}

// 破坏验证 ①：把 redactedKeys 删空后本用例必须红。
func TestRedactMasksSensitiveFields(t *testing.T) {
	payload := json.RawMessage(`{
		"instrument":"600519.SH",
		"token":"abc123",
		"access_token":"xyz",
		"nested":{"API-KEY":"k-1","password":123456,"keep":"ok"},
		"items":[{"refreshToken":"r-1","name":"row"}]
	}`)
	out, err := Redact(payload)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	text := string(out)
	for _, leaked := range []string{"abc123", "xyz", "k-1", "123456", "r-1"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("敏感值 %q 未被遮蔽：%s", leaked, text)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("脱敏结果不是合法 JSON：%v", err)
	}
	if decoded["token"] != RedactedPlaceholder {
		t.Fatalf("token 应被替换为 %q，得到 %#v", RedactedPlaceholder, decoded["token"])
	}
	nested := decoded["nested"].(map[string]any)
	if nested["API-KEY"] != RedactedPlaceholder || nested["password"] != RedactedPlaceholder {
		t.Fatalf("嵌套敏感键未遮蔽：%#v", nested)
	}
	if nested["keep"] != "ok" {
		t.Fatalf("非敏感字段不应被改动：%#v", nested)
	}
}

func TestRedactThenHashIsStable(t *testing.T) {
	one := json.RawMessage(`{"token":"a","instrument":"600519.SH"}`)
	two := json.RawMessage(`{"instrument":"600519.SH","token":"b"}`)

	ro, err := Redact(one)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := Redact(two)
	if err != nil {
		t.Fatal(err)
	}
	ho, _ := CanonicalHash(ro)
	ht, _ := CanonicalHash(rt)
	if ho != ht {
		t.Fatalf("脱敏后同内容应得同一哈希：%s != %s", ho, ht)
	}
}

// 破坏验证 ②：RetentionDays 接受 0 / 负数后本用例必须红。
func TestRetentionDaysRejectsNonPositive(t *testing.T) {
	for _, raw := range []string{"0", "-1", "-365", "abc", "1.5"} {
		if _, err := RetentionDays(raw); err == nil {
			t.Fatalf("RetentionDays(%q) 应报错", raw)
		}
	}
}

func TestRetentionDaysDefaults(t *testing.T) {
	days, err := RetentionDays("")
	if err != nil {
		t.Fatal(err)
	}
	if days != DefaultRetentionDays {
		t.Fatalf("空值应取默认 %d 天，得到 %d", DefaultRetentionDays, days)
	}
	days, err = RetentionDays("30")
	if err != nil {
		t.Fatal(err)
	}
	if days != 30 {
		t.Fatalf("应解析为 30 天，得到 %d", days)
	}
}

func TestRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	got := RetentionCutoff(now, 30)
	want := now.Add(-30 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("cutoff 应为 %s，得到 %s", want, got)
	}
}

func TestValidateWriteRequest(t *testing.T) {
	asOf := time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC)
	valid := WriteRequest{Source: "e2e", Dataset: "d", Key: "k", AsOf: &asOf, Payload: json.RawMessage(`{"a":1}`)}
	if err := valid.Validate(); err != nil {
		t.Fatalf("合法请求不应报错：%v", err)
	}
	badHash := valid
	badHash.ContentHash = strings.Repeat("A", 64)
	if err := badHash.Validate(); err == nil {
		t.Fatal("大写/非 hex 的 content_hash 应报错")
	}
	noPayload := valid
	noPayload.Payload = nil
	if err := noPayload.Validate(); err == nil {
		t.Fatal("缺 payload 应报错")
	}
}
