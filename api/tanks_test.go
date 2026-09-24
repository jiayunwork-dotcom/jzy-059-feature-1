package api

import (
	"math"
	"net/http"
	"testing"
)

// 内联参数 + 部分注液舱：响应须同时给出固体 GM、总扣减、有效 GM，
// 且判定落在有效 GM 上。
func TestEvaluate_WithInlineTanks(t *testing.T) {
	r, _ := newTestRouter(t)
	// i = 10·4³/12 = 53.333，δ = 850·53.333/(1025·1000) ≈ 0.044227 m。
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": map[string]any{
			"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
		},
		"tanks": []map[string]any{
			{"name": "fuel", "length": 10, "width": 4, "liquidDensity": 850, "status": "partial"},
		},
		"angleDeg": 10,
	})
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	res := body["result"].(map[string]any)
	solid := res["solidGM"].(float64)
	delta := res["freeSurfaceCorrection"].(float64)
	eff := res["effectiveGM"].(float64)
	if math.Abs(solid-4.2) > 1e-9 {
		t.Fatalf("solidGM 应为 4.2，得到 %v", solid)
	}
	wantDelta := 850.0 * (10.0 * 64 / 12.0) / (1025.0 * 1000.0)
	if math.Abs(delta-wantDelta) > 1e-9 {
		t.Fatalf("总扣减 = %v，期望 %v", delta, wantDelta)
	}
	if math.Abs(eff-(4.2-wantDelta)) > 1e-9 {
		t.Fatalf("effectiveGM = %v，期望 %v", eff, 4.2-wantDelta)
	}
	if math.Abs(res["gm"].(float64)-eff) > 1e-12 {
		t.Fatalf("对外 gm 应等于有效 GM")
	}
	wantGZ := eff * math.Sin(10.0*math.Pi/180)
	if math.Abs(res["gz"].(float64)-wantGZ) > 1e-9 {
		t.Fatalf("GZ 应建立在有效 GM 上: got %v want %v", res["gz"], wantGZ)
	}
	if body["stability"] != "positive" || body["positive"] != true {
		t.Fatalf("应判 positive: %v", body)
	}
	details := res["tankCorrections"].([]any)
	if len(details) != 1 {
		t.Fatalf("应有 1 条舱明细，得到 %d", len(details))
	}
	d0 := details[0].(map[string]any)
	if d0["status"] != "partial" || math.Abs(d0["correction"].(float64)-wantDelta) > 1e-9 {
		t.Fatalf("舱明细异常: %v", d0)
	}
}

// 端到端翻盘：固体正稳性，挂上宽舱后有效 GM 为负、判定翻 negative。
func TestEvaluate_TanksFlipStabilityNegative(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": map[string]any{
			"displacementVolume": 1000, "kb": 1.2, "kg": 6.0, "transverseInertia": 5000,
		},
		"tanks": []map[string]any{
			{"name": "wide-wing-tank", "length": 20, "width": 10, "liquidDensity": 1000, "status": "partial"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	res := body["result"].(map[string]any)
	if res["solidGM"].(float64) <= 0 {
		t.Fatalf("固体 GM 应为正，得到 %v", res["solidGM"])
	}
	if res["effectiveGM"].(float64) >= 0 {
		t.Fatalf("有效 GM 应为负，得到 %v", res["effectiveGM"])
	}
	if body["stability"] != "negative" || body["positive"] != false {
		t.Fatalf("最终判定必须为 negative: %v", body)
	}
}

// 空舱/满舱状态下扣减为零；直给惯性矩也能跑通。
func TestEvaluate_EmptyFullAndDirectInertia(t *testing.T) {
	r, _ := newTestRouter(t)
	baseInline := map[string]any{
		"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
	}
	for _, status := range []string{"empty", "full"} {
		_, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
			"params": baseInline,
			"tanks":  []map[string]any{{"name": "t", "length": 10, "width": 4, "liquidDensity": 850, "status": status}},
		})
		res := body["result"].(map[string]any)
		if res["freeSurfaceCorrection"].(float64) != 0 ||
			math.Abs(res["effectiveGM"].(float64)-4.2) > 1e-12 ||
			math.Abs(res["gm"].(float64)-4.2) > 1e-12 {
			t.Fatalf("%s 舱不应产生扣减: %v", status, res)
		}
	}

	// 直给惯性矩：i = 53.333... 与 10×4 的矩形等价。
	_, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": baseInline,
		"tanks":  []map[string]any{{"name": "direct", "freeSurfaceInertia": 10.0 * 64 / 12.0, "liquidDensity": 850, "status": "partial"}},
	})
	res := body["result"].(map[string]any)
	wantDelta := 850.0 * (10.0 * 64 / 12.0) / (1025.0 * 1000.0)
	if math.Abs(res["freeSurfaceCorrection"].(float64)-wantDelta) > 1e-9 {
		t.Fatalf("直给惯性矩扣减异常: %v", res["freeSurfaceCorrection"])
	}
}

// 非法液舱字段 → 400 且 reason 指出舱位与字段。
func TestEvaluate_BadTanksRejected(t *testing.T) {
	r, _ := newTestRouter(t)
	cases := []map[string]any{
		// 宽度非正
		{"params": map[string]any{"displacementVolume": 1000, "kb": 1, "kg": 2, "transverseInertia": 5000},
			"tanks": []map[string]any{{"name": "t", "length": 10, "width": 0, "liquidDensity": 850, "status": "partial"}}},
		// 密度非正
		{"params": map[string]any{"displacementVolume": 1000, "kb": 1, "kg": 2, "transverseInertia": 5000},
			"tanks": []map[string]any{{"name": "t", "length": 10, "width": 4, "liquidDensity": 0, "status": "partial"}}},
		// 状态不在允许集合
		{"params": map[string]any{"displacementVolume": 1000, "kb": 1, "kg": 2, "transverseInertia": 5000},
			"tanks": []map[string]any{{"name": "t", "length": 10, "width": 4, "liquidDensity": 850, "status": "80%"}}},
		// 长宽与惯性矩冲突
		{"params": map[string]any{"displacementVolume": 1000, "kb": 1, "kg": 2, "transverseInertia": 5000},
			"tanks": []map[string]any{{"name": "t", "length": 10, "width": 4, "freeSurfaceInertia": 50, "liquidDensity": 850, "status": "partial"}}},
	}
	for i, payload := range cases {
		code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", payload)
		if code != http.StatusBadRequest {
			t.Fatalf("用例 #%d 应 400，得到 %d: %v", i, code, body)
		}
		reason, _ := body["reason"].(string)
		if reason == "" || !containsAny(reason, "液舱") {
			t.Fatalf("用例 #%d 的 reason 应指出液舱，得到 %q", i, reason)
		}
	}
}

// 带舱建档 → 凭名取回时液舱一起还原并直接算出修正结果；
// 显式 tanks: [] 可临时按无舱核算，且不改动档案本身。
func TestCondition_TanksLifecycleAndOverride(t *testing.T) {
	r, _ := newTestRouter(t)

	// PUT 建档，带 1 个部分注液宽舱（KG=6，固体 GM=0.2 → 挂舱转负）。
	code, body := doJSON(t, r, http.MethodPut, "/api/v1/conditions/loaded-tanks", map[string]any{
		"displacementVolume": 1000, "kb": 1.2, "kg": 6.0, "transverseInertia": 5000,
		"tanks": []map[string]any{
			{"name": "wide-wing-tank", "length": 20, "width": 10, "liquidDensity": 1000, "status": "partial"},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("带舱建档失败: %d %v", code, body)
	}

	// GET 取回：液舱随档案还原。
	_, body = doJSON(t, r, http.MethodGet, "/api/v1/conditions/loaded-tanks", nil)
	cond := body["condition"].(map[string]any)
	tanks := cond["tanks"].([]any)
	if len(tanks) != 1 || tanks[0].(map[string]any)["name"] != "wide-wing-tank" {
		t.Fatalf("取回的液舱清单异常: %v", tanks)
	}

	// 按名核算：不带 tanks 字段 → 使用档案自带液舱 → 负稳性。
	_, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "loaded-tanks",
	})
	res := body["result"].(map[string]any)
	if body["stability"] != "negative" {
		t.Fatalf("档案宽舱应把稳性扣成负，得到 %v (eff=%v solid=%v)",
			body["stability"], res["effectiveGM"], res["solidGM"])
	}
	if res["freeSurfaceCorrection"].(float64) <= 0 {
		t.Fatal("应存在正扣减")
	}

	// 显式空数组：本次按刚体算，应为正，且不改动档案。
	_, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "loaded-tanks",
		"tanks":         []any{},
	})
	res = body["result"].(map[string]any)
	if body["stability"] != "positive" || res["freeSurfaceCorrection"].(float64) != 0 {
		t.Fatalf("显式空舱清单应临时按刚体正稳核算: %v", body)
	}
	_, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "loaded-tanks",
	})
	if body["stability"] != "negative" {
		t.Fatal("临时覆盖不应改动档案自带液舱")
	}

	// 建档时非法液舱 → 400 且不落库。
	code, _ = doJSON(t, r, http.MethodPost, "/api/v1/conditions", map[string]any{
		"name": "bad-tanks", "displacementVolume": 1000, "kb": 1.2, "kg": 2, "transverseInertia": 5000,
		"tanks": []map[string]any{{"name": "x", "length": -1, "width": 4, "liquidDensity": 850, "status": "partial"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("非法液舱建档应 400，得到 %d", code)
	}
	code, _ = doJSON(t, r, http.MethodGet, "/api/v1/conditions/bad-tanks", nil)
	if code != http.StatusNotFound {
		t.Fatalf("被拦截的档案不应落库，得到 %d", code)
	}
}

// 带舱扫描：曲线头部与逐点 GZ 都建立在有效 GM 上。
func TestScan_WithTanksUsesEffectiveCurve(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/scan", map[string]any{
		"params": map[string]any{
			"displacementVolume": 1000, "kb": 1.2, "kg": 6.0, "transverseInertia": 5000,
		},
		"tanks": []map[string]any{
			{"name": "wide", "length": 20, "width": 10, "liquidDensity": 1000, "status": "partial"},
		},
		"startDeg": 0, "endDeg": 10, "stepDeg": 5,
	})
	if code != http.StatusOK {
		t.Fatalf("扫描失败: %d %v", code, body)
	}
	if body["stability"] != "negative" {
		t.Fatalf("头部判定应基于有效 GM 为 negative: %v", body)
	}
	eff := body["effectiveGM"].(float64)
	if eff >= 0 || math.Abs(body["solidGM"].(float64)-0.2) > 1e-9 {
		t.Fatalf("头部 GM 字段异常: solid=%v eff=%v", body["solidGM"], eff)
	}
	points := body["points"].([]any)
	if len(points) != 3 {
		t.Fatalf("应有 3 个点，得到 %d", len(points))
	}
	p0 := points[0].(map[string]any)
	if p0["gz"].(float64) != 0 || p0["moment"].(float64) != 0 {
		t.Fatalf("0° 点 GZ/力矩必须为零: %v", p0)
	}
	for i, pa := range points {
		pt := pa.(map[string]any)
		deg := float64(i * 5)
		wantGZ := eff * math.Sin(deg*math.Pi/180)
		if math.Abs(pt["gz"].(float64)-wantGZ) > 1e-9 {
			t.Fatalf("点 %v 的 GZ 应基于有效 GM: got %v want %v", deg, pt["gz"], wantGZ)
		}
	}
}

func containsAny(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
