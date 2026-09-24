package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"shipstability/internal/archive"
	"shipstability/internal/store"
)

func newTestRouter(t *testing.T) (*gin.Engine, *archive.Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	fs, err := store.NewFileStore(t.TempDir() + "/conditions.json")
	if err != nil {
		t.Fatalf("存储初始化失败: %v", err)
	}
	svc := archive.NewService(fs)
	if err := svc.SeedDefaults(); err != nil {
		t.Fatalf("预置失败: %v", err)
	}
	return NewServer(svc).Router(), svc
}

func doJSON(t *testing.T, r http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON: %v (body=%s)", err, w.Body.String())
		}
	}
	return w.Code, out
}

func TestHealth(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodGet, "/health", nil)
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("健康检查异常: code=%d body=%v", code, body)
	}
}

func TestEvaluate_InlinePositive(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": map[string]any{
			"displacementVolume": 1000,
			"kb":                 1.2,
			"kg":                 2.0,
			"transverseInertia":  5000,
		},
		"angleDeg": 10,
	})
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	if body["stability"] != "positive" || body["positive"] != true {
		t.Fatalf("应判正稳性: %v", body)
	}
	res, _ := body["result"].(map[string]any)
	if math.Abs(res["bm"].(float64)-5.0) > 1e-9 {
		t.Fatalf("BM 异常: %v", res["bm"])
	}
	if math.Abs(res["gm"].(float64)-4.2) > 1e-9 {
		t.Fatalf("GM 异常: %v", res["gm"])
	}
	// 端到端核对度→弧度换算与 GZ。
	wantRad := 10.0 * math.Pi / 180.0
	if math.Abs(res["angleRad"].(float64)-wantRad) > 1e-12 {
		t.Fatalf("angleRad 异常: %v", res["angleRad"])
	}
	wantGZ := 4.2 * math.Sin(wantRad)
	if math.Abs(res["gz"].(float64)-wantGZ) > 1e-9 {
		t.Fatalf("GZ 异常（疑似单位没换算）: got %v want %v", res["gz"], wantGZ)
	}
}

func TestEvaluate_NegativeAndZero(t *testing.T) {
	r, _ := newTestRouter(t)
	// 负稳性
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": map[string]any{
			"displacementVolume": 1000, "kb": 1.2, "kg": 100, "transverseInertia": 5000,
		},
		"angleDeg": 5,
	})
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	if body["stability"] != "negative" || body["positive"] != false {
		t.Fatalf("应判负稳性: %v", body)
	}

	// 零横倾：GZ 与力矩恒为零（省略 angleDeg 即默认 0°）
	_, body0 := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": map[string]any{
			"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
		},
	})
	res := body0["result"].(map[string]any)
	if res["gz"].(float64) != 0 || res["rightingMoment"].(float64) != 0 {
		t.Fatalf("0° 时 GZ/力矩应为零: %v", res)
	}
}

func TestEvaluate_OverAngleWarning(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": map[string]any{
			"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
		},
		"angleDeg": 25,
	})
	if code != http.StatusOK {
		t.Fatalf("超界角度仍应 200 给值，得到 %d", code)
	}
	res := body["result"].(map[string]any)
	if res["smallAngle"].(bool) {
		t.Fatal("25° 不应标记为小倾角")
	}
	if w, _ := res["warning"].(string); w == "" {
		t.Fatal("25° 必须附越界提醒")
	}
}

func TestEvaluate_ValidationErrors(t *testing.T) {
	r, _ := newTestRouter(t)
	cases := []map[string]any{
		// 排水体积非正
		{"params": map[string]any{"displacementVolume": 0, "kb": 1, "kg": 2, "transverseInertia": 1}, "angleDeg": 1},
		// 惯性矩非正
		{"params": map[string]any{"displacementVolume": 10, "kb": 1, "kg": 2, "transverseInertia": -3}, "angleDeg": 1},
		// 高度不合物理约定
		{"params": map[string]any{"displacementVolume": 10, "kb": -1, "kg": 2, "transverseInertia": 3}, "angleDeg": 1},
		// 既无档名又无参数
		{"angleDeg": 1},
		// 档名与参数同时给出
		{"conditionName": "rectangular-barge", "params": map[string]any{"displacementVolume": 1}, "angleDeg": 1},
		// 引用不存在的档
		{"conditionName": "ghost", "angleDeg": 1},
	}
	for i, payload := range cases {
		code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", payload)
		if code == http.StatusOK {
			t.Fatalf("用例 #%d 应被拦截，却 200: %v", i, body)
		}
		if code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Fatalf("用例 #%d 应返回 400/404，得到 %d", i, code)
		}
		if _, ok := body["reason"]; !ok {
			t.Fatalf("用例 #%d 错误响应必须带原因: %v", i, body)
		}
	}

	// 坏 JSON
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stability/evaluate", bytes.NewBufferString("{not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400，得到 %d", w.Code)
	}
}

func TestScanEndpoint_RealCurvePoints(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/scan", map[string]any{
		"conditionName": "rectangular-barge",
		"startDeg":      0,
		"endDeg":        10,
		"stepDeg":       2,
	})
	if code != http.StatusOK {
		t.Fatalf("扫描失败: %d %v", code, body)
	}
	points, _ := body["points"].([]any)
	if len(points) != 6 {
		t.Fatalf("应有 6 个点（0,2,4,6,8,10），得到 %d", len(points))
	}
	gm := body["gm"].(float64)
	if math.Abs(gm-4.05) > 1e-9 {
		t.Fatalf("驳船 GM 应为 4.05，得到 %v", gm)
	}
	for i, ptAny := range points {
		pt := ptAny.(map[string]any)
		deg := float64(i * 2)
		if math.Abs(pt["angleDeg"].(float64)-deg) > 1e-9 {
			t.Fatalf("点 %d 角度异常: %v", i, pt["angleDeg"])
		}
		wantGZ := gm * math.Sin(deg*math.Pi/180)
		if math.Abs(pt["gz"].(float64)-wantGZ) > 1e-9 {
			t.Fatalf("点 %d GZ 与公式不符: got %v want %v", i, pt["gz"], wantGZ)
		}
	}

	// 扫描缺步长 → 400
	code, _ = doJSON(t, r, http.MethodPost, "/api/v1/stability/scan", map[string]any{
		"conditionName": "rectangular-barge", "startDeg": 0, "endDeg": 10,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 stepDeg 应 400，得到 %d", code)
	}
}

func TestConditionLifecycle(t *testing.T) {
	r, _ := newTestRouter(t)

	// 预置算例拉起即可核对
	code, body := doJSON(t, r, http.MethodGet, "/api/v1/conditions/rectangular-barge", nil)
	if code != http.StatusOK {
		t.Fatalf("取预置档失败: %d %v", code, body)
	}
	cond := body["condition"].(map[string]any)
	if math.Abs(cond["transverseInertia"].(float64)-5760) > 1e-9 {
		t.Fatalf("预置 IT 应为 5760: %v", cond["transverseInertia"])
	}

	// POST 建档
	code, body = doJSON(t, r, http.MethodPost, "/api/v1/conditions", map[string]any{
		"name":               "loaded-departure",
		"displacementVolume": 1500,
		"kb":                 1.4,
		"kg":                 3.0,
		"transverseInertia":  7000,
		"waterDensity":       1025,
	})
	if code != http.StatusCreated {
		t.Fatalf("建档失败: %d %v", code, body)
	}

	// 立即凭名字取回重算
	code, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "loaded-departure",
		"angleDeg":      0,
	})
	if code != http.StatusOK {
		t.Fatalf("按名重算失败: %d %v", code, body)
	}
	res := body["result"].(map[string]any)
	wantGM := 1.4 + 7000.0/1500.0 - 3.0
	if math.Abs(res["gm"].(float64)-wantGM) > 1e-9 {
		t.Fatalf("按名重算 GM 不符: got %v want %v", res["gm"], wantGM)
	}

	// PUT 覆盖：压载调整后 KG 下降
	code, _ = doJSON(t, r, http.MethodPut, "/api/v1/conditions/loaded-departure", map[string]any{
		"displacementVolume": 1500,
		"kb":                 1.4,
		"kg":                 2.4,
		"transverseInertia":  7000,
	})
	if code != http.StatusOK {
		t.Fatalf("覆盖失败: %d", code)
	}
	_, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "loaded-departure",
	})
	res = body["result"].(map[string]any)
	if math.Abs(res["gm"].(float64)-(wantGM+0.6)) > 1e-9 {
		t.Fatalf("压载后 GM 应上升 0.6: %v", res["gm"])
	}

	// 列表含预置档与自建档
	_, body = doJSON(t, r, http.MethodGet, "/api/v1/conditions", nil)
	conds := body["conditions"].([]any)
	if len(conds) != 2 {
		t.Fatalf("应列出 2 条档，得到 %d", len(conds))
	}

	// 删除
	code, _ = doJSON(t, r, http.MethodDelete, "/api/v1/conditions/loaded-departure", nil)
	if code != http.StatusOK {
		t.Fatalf("删除失败: %d", code)
	}
	code, _ = doJSON(t, r, http.MethodGet, "/api/v1/conditions/loaded-departure", nil)
	if code != http.StatusNotFound {
		t.Fatalf("删除后应 404，得到 %d", code)
	}
}

// 宽舱构造：内联浮态用基准船（固体 GM=4.2）。
func inlinePartialTank(name string, length, width, density float64) map[string]any {
	return map[string]any{
		"name":          name,
		"length":        length,
		"width":         width,
		"liquidDensity": density,
		"fillingStatus": "partial",
	}
}

func inlineBaseParams() map[string]any {
	return map[string]any{
		"displacementVolume": 1000,
		"kb":                 1.2,
		"kg":                 2.0,
		"transverseInertia":  5000,
	}
}

// 内联液舱的单点核算：响应把三笔账与逐舱明细写明，GZ 按有效 GM。
func TestEvaluate_WithInlineTanks(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params":   inlineBaseParams(),
		"angleDeg": 5,
		"tanks": []map[string]any{
			inlinePartialTank("fuel", 4, 2, 900),
		},
	})
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	res := body["result"].(map[string]any)
	if math.Abs(res["solidGm"].(float64)-4.2) > 1e-9 {
		t.Fatalf("固体 GM 应为 4.2: %v", res["solidGm"])
	}
	wantCorr := 900.0 * (4.0 * 8.0 / 12.0) / (1025.0 * 1000.0)
	if math.Abs(res["freeSurfaceCorrection"].(float64)-wantCorr) > 1e-12 {
		t.Fatalf("总扣减异常: got %v want %v", res["freeSurfaceCorrection"], wantCorr)
	}
	wantEff := 4.2 - wantCorr
	if math.Abs(res["effectiveGm"].(float64)-wantEff) > 1e-9 {
		t.Fatalf("有效 GM 异常: %v", res["effectiveGm"])
	}
	if math.Abs(res["gm"].(float64)-wantEff) > 1e-9 {
		t.Fatalf("gm 应为修正后的值: %v", res["gm"])
	}
	if body["stability"] != "positive" || body["positive"] != true {
		t.Fatalf("仍应判正稳性: %v", body)
	}
	wantGZ := wantEff * math.Sin(5*math.Pi/180)
	if math.Abs(res["gz"].(float64)-wantGZ) > 1e-9 {
		t.Fatalf("GZ 必须按有效 GM: got %v want %v", res["gz"], wantGZ)
	}
	tanks := res["tankCorrections"].([]any)
	if len(tanks) != 1 {
		t.Fatalf("应有一条舱明细: %v", tanks)
	}
	d := tanks[0].(map[string]any)
	if math.Abs(d["correction"].(float64)-wantCorr) > 1e-12 || d["fillingStatus"] != "partial" {
		t.Fatalf("舱明细异常: %v", d)
	}
}

// 空舱/满舱扣减为零；只改状态，同一几何立刻不再折损稳性。
func TestEvaluate_EmptyAndFullTanksNoCorrection(t *testing.T) {
	r, _ := newTestRouter(t)
	for _, status := range []string{"empty", "full"} {
		tank := inlinePartialTank("fuel", 4, 2, 900)
		tank["fillingStatus"] = status
		_, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
			"params":   inlineBaseParams(),
			"angleDeg": 5,
			"tanks":    []map[string]any{tank},
		})
		res := body["result"].(map[string]any)
		if res["freeSurfaceCorrection"].(float64) != 0 {
			t.Fatalf("%s 舱扣减应为零: %v", status, res["freeSurfaceCorrection"])
		}
		if math.Abs(res["effectiveGm"].(float64)-4.2) > 1e-12 {
			t.Fatalf("%s 舱有效 GM 应等于固体 GM: %v", status, res["effectiveGm"])
		}
		if body["stability"] != "positive" {
			t.Fatalf("%s 舱不应改变正稳性判定", status)
		}
	}
}

// 端到端翻转：不带舱 positive，挂上足够多宽舱后判定翻成 negative。
func TestEvaluate_TanksFlipStabilityToNegative(t *testing.T) {
	r, _ := newTestRouter(t)
	// 先确认不带舱是正稳性。
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": inlineBaseParams(), "angleDeg": 5,
	})
	if code != 200 || body["stability"] != "positive" {
		t.Fatalf("前置：应正稳性，得到 %d %v", code, body)
	}

	tanks := []map[string]any{
		inlinePartialTank("w1", 2, 22, 1000),
		inlinePartialTank("w2", 2, 22, 1000),
		inlinePartialTank("w3", 2, 22, 1000),
	}
	code, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": inlineBaseParams(), "angleDeg": 5, "tanks": tanks,
	})
	if code != http.StatusOK {
		t.Fatalf("期望 200，得到 %d: %v", code, body)
	}
	if body["stability"] != "negative" || body["positive"] != false {
		t.Fatalf("挂舱后必须翻成负稳性: %v", body)
	}
	res := body["result"].(map[string]any)
	if res["solidGm"].(float64) <= 0 || res["effectiveGm"].(float64) >= 0 {
		t.Fatalf("固体 GM 为正、有效 GM 应为负: solid=%v eff=%v",
			res["solidGm"], res["effectiveGm"])
	}
	if res["gz"].(float64) >= 0 {
		t.Fatalf("正横倾下负稳性 GZ 应为负: %v", res["gz"])
	}

	// 同一批舱灌满：稳性回到正，证明翻转完全由自由液面造成。
	for _, tk := range tanks {
		tk["fillingStatus"] = "full"
	}
	_, bodyFull := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": inlineBaseParams(), "angleDeg": 5, "tanks": tanks,
	})
	if bodyFull["stability"] != "positive" {
		t.Fatalf("灌满后应回到正稳性: %v", bodyFull)
	}
}

// 0° 横倾 GZ 恒为零，即使挂舱后有效 GM 已为负。
func TestEvaluate_TanksZeroAngleGZZero(t *testing.T) {
	r, _ := newTestRouter(t)
	tanks := []map[string]any{
		inlinePartialTank("w1", 2, 22, 1000),
		inlinePartialTank("w2", 2, 22, 1000),
		inlinePartialTank("w3", 2, 22, 1000),
	}
	_, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"params": inlineBaseParams(), "tanks": tanks, // angleDeg 省略 = 0
	})
	res := body["result"].(map[string]any)
	if res["gz"].(float64) != 0 || res["rightingMoment"].(float64) != 0 {
		t.Fatalf("0° 时 GZ/力矩必须恒零: %v", res)
	}
	if body["stability"] != "negative" {
		t.Fatalf("该装载应为负稳性（证明确实挂了舱）: %v", body)
	}
}

// 带舱扫描：头部三笔账齐全，整条曲线按有效 GM。
func TestScanEndpoint_WithTanks(t *testing.T) {
	r, _ := newTestRouter(t)
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/scan", map[string]any{
		"params":   inlineBaseParams(),
		"tanks":    []map[string]any{inlinePartialTank("fuel", 4, 2, 900)},
		"startDeg": 0,
		"endDeg":   10,
		"stepDeg":  2,
	})
	if code != http.StatusOK {
		t.Fatalf("扫描失败: %d %v", code, body)
	}
	wantCorr := 900.0 * (4.0 * 8.0 / 12.0) / (1025.0 * 1000.0)
	wantEff := 4.2 - wantCorr
	if math.Abs(body["solidGm"].(float64)-4.2) > 1e-9 {
		t.Fatalf("固体 GM 异常: %v", body["solidGm"])
	}
	if math.Abs(body["effectiveGm"].(float64)-wantEff) > 1e-9 {
		t.Fatalf("有效 GM 异常: %v", body["effectiveGm"])
	}
	if math.Abs(body["gm"].(float64)-wantEff) > 1e-9 {
		t.Fatalf("gm 应为有效 GM: %v", body["gm"])
	}
	points := body["points"].([]any)
	for i, p := range points {
		pt := p.(map[string]any)
		deg := float64(i * 2)
		wantGZ := wantEff * math.Sin(deg*math.Pi/180)
		if math.Abs(pt["gz"].(float64)-wantGZ) > 1e-9 {
			t.Fatalf("点 %d GZ 未按有效 GM: got %v want %v", i, pt["gz"], wantGZ)
		}
	}
}

// 液舱建档、取回、按名修正核算、覆盖改舱、删档的完整生命周期。
func TestConditionLifecycle_WithTanks(t *testing.T) {
	r, _ := newTestRouter(t)

	// 建档时登记两个部分注液舱。
	tanks := []map[string]any{
		inlinePartialTank("fuel", 4, 2, 900),
		inlinePartialTank("ballast", 6, 3, 1000),
	}
	code, body := doJSON(t, r, http.MethodPut, "/api/v1/conditions/tanker", map[string]any{
		"displacementVolume": 1000,
		"kb":                 1.2,
		"kg":                 2.0,
		"transverseInertia":  5000,
		"waterDensity":       1025,
		"tanks":              tanks,
	})
	if code != http.StatusOK {
		t.Fatalf("带舱建档失败: %d %v", code, body)
	}

	// 取档：液舱清单原样还原。
	_, body = doJSON(t, r, http.MethodGet, "/api/v1/conditions/tanker", nil)
	cond := body["condition"].(map[string]any)
	gotTanks := cond["tanks"].([]any)
	if len(gotTanks) != 2 || gotTanks[1].(map[string]any)["name"] != "ballast" {
		t.Fatalf("取回的液舱清单异常: %v", gotTanks)
	}

	// 按名核算：自动带上档案内液舱做修正。
	_, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "tanker",
		"angleDeg":      0,
	})
	res := body["result"].(map[string]any)
	wantCorr := 900.0*(4.0*8.0/12.0)/(1025.0*1000.0) +
		1000.0*(6.0*27.0/12.0)/(1025.0*1000.0)
	if math.Abs(res["freeSurfaceCorrection"].(float64)-wantCorr) > 1e-12 {
		t.Fatalf("按名核算总扣减异常: got %v want %v", res["freeSurfaceCorrection"], wantCorr)
	}
	if math.Abs(res["solidGm"].(float64)-4.2) > 1e-9 {
		t.Fatalf("固体 GM 应为 4.2: %v", res["solidGm"])
	}
	if math.Abs(res["effectiveGm"].(float64)-(4.2-wantCorr)) > 1e-9 {
		t.Fatalf("有效 GM 异常: %v", res["effectiveGm"])
	}

	// 覆盖：把两个舱都改成满舱。
	fullTanks := []map[string]any{
		{"name": "fuel", "length": 4.0, "width": 2.0, "liquidDensity": 900.0, "fillingStatus": "full"},
		{"name": "ballast", "length": 6.0, "width": 3.0, "liquidDensity": 1000.0, "fillingStatus": "full"},
	}
	code, _ = doJSON(t, r, http.MethodPut, "/api/v1/conditions/tanker", map[string]any{
		"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
		"tanks": fullTanks,
	})
	if code != http.StatusOK {
		t.Fatalf("覆盖档案失败: %d", code)
	}
	_, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "tanker",
	})
	res = body["result"].(map[string]any)
	if res["freeSurfaceCorrection"].(float64) != 0 {
		t.Fatalf("灌满后扣减应为零: %v", res["freeSurfaceCorrection"])
	}

	// 覆盖成不带 tanks：档案退回纯固体，响应不再有修正字段。
	doJSON(t, r, http.MethodPut, "/api/v1/conditions/tanker", map[string]any{
		"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
	})
	_, body = doJSON(t, r, http.MethodGet, "/api/v1/conditions/tanker", nil)
	if tanks, ok := body["condition"].(map[string]any)["tanks"]; ok && tanks != nil {
		t.Fatalf("清空舱单后不应再返回 tanks: %v", tanks)
	}

	// 删除：整档含舱消失。
	code, _ = doJSON(t, r, http.MethodDelete, "/api/v1/conditions/tanker", nil)
	if code != http.StatusOK {
		t.Fatalf("删除失败: %d", code)
	}
	code, _ = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "tanker",
	})
	if code != http.StatusNotFound {
		t.Fatalf("删档后按名核算应 404，得到 %d", code)
	}
}

// 液舱校验错误一律 400 且带中文原因，点得清哪个字段。
func TestEvaluate_BadTankRejected(t *testing.T) {
	r, _ := newTestRouter(t)
	cases := []map[string]any{
		// 状态取值不在允许集合
		{"params": inlineBaseParams(), "tanks": []map[string]any{
			{"name": "t", "length": 4.0, "width": 2.0, "liquidDensity": 900.0, "fillingStatus": "half"}}},
		// 缺注液状态
		{"params": inlineBaseParams(), "tanks": []map[string]any{
			{"name": "t", "length": 4.0, "width": 2.0, "liquidDensity": 900.0}}},
		// 密度非正
		{"params": inlineBaseParams(), "tanks": []map[string]any{
			{"name": "t", "length": 4.0, "width": 2.0, "liquidDensity": 0.0, "fillingStatus": "partial"}}},
		// 尺寸非正（宽为零且未给惯性矩）
		{"params": inlineBaseParams(), "tanks": []map[string]any{
			{"name": "t", "length": 4.0, "width": 0.0, "liquidDensity": 900.0, "fillingStatus": "partial"}}},
		// 直给惯性矩为负
		{"params": inlineBaseParams(), "tanks": []map[string]any{
			{"name": "t", "freeSurfaceInertia": -2.0, "liquidDensity": 900.0, "fillingStatus": "partial"}}},
	}
	for i, payload := range cases {
		code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", payload)
		if code != http.StatusBadRequest {
			t.Fatalf("用例 #%d 应 400，得到 %d: %v", i, code, body)
		}
		reason, _ := body["reason"].(string)
		if reason == "" {
			t.Fatalf("用例 #%d 必须带中文原因", i)
		}
	}

	// 建档携带非法舱同样 400。
	code, body := doJSON(t, r, http.MethodPost, "/api/v1/conditions", map[string]any{
		"name":               "bad-tanker",
		"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
		"tanks": []map[string]any{
			{"name": "t", "length": 4.0, "width": 2.0, "liquidDensity": -1.0, "fillingStatus": "partial"},
		},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("非法舱建档应 400，得到 %d: %v", code, body)
	}
	// 被拒档案不得落库。
	if code, _ := doJSON(t, r, http.MethodGet, "/api/v1/conditions/bad-tanker", nil); code != http.StatusNotFound {
		t.Fatalf("被拒档案不应落库，得到 %d", code)
	}

	// 引用档名同时内联液舱：拒绝，避免舱来源含糊。
	code, body = doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
		"conditionName": "rectangular-barge",
		"tanks":         []map[string]any{inlinePartialTank("x", 4, 2, 900)},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("档名与 tanks 同给应 400，得到 %d: %v", code, body)
	}
}

// 不同档案各带各的舱，并发按名核算互不串舱。
func TestHTTP_ConcurrentTankIsolation(t *testing.T) {
	r, _ := newTestRouter(t)
	const n = 30
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("tanker-%02d", i)
		width := 1.0 + float64(i)*0.2 // 舱宽各异 ⇒ 扣减各异
		code, _ := doJSON(t, r, http.MethodPut, "/api/v1/conditions/"+name, map[string]any{
			"displacementVolume": 1000, "kb": 1.2, "kg": 2.0, "transverseInertia": 5000,
			"tanks": []map[string]any{inlinePartialTank("only", 4, width, 900)},
		})
		if code != http.StatusOK {
			t.Fatalf("建档 %s 失败: %d", name, code)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan string, 2*n)
	for i := 0; i < n; i++ {
		i := i
		name := fmt.Sprintf("tanker-%02d", i)
		width := 1.0 + float64(i)*0.2
		wantCorr := 900.0 * (4.0 * width * width * width / 12.0) / (1025.0 * 1000.0)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
				"conditionName": name,
				"angleDeg":      5,
			})
			res := body["result"].(map[string]any)
			if math.Abs(res["freeSurfaceCorrection"].(float64)-wantCorr) > 1e-9 {
				errCh <- fmt.Sprintf("%s 扣减串舱: got %v want %v",
					name, res["freeSurfaceCorrection"], wantCorr)
			}
			if math.Abs(res["solidGm"].(float64)-4.2) > 1e-9 {
				errCh <- fmt.Sprintf("%s 固体 GM 异常: %v", name, res["solidGm"])
			}
		}()
	}
	// 不带舱的预置档被并发重算：永远不出现修正字段。
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
				"conditionName": "rectangular-barge",
				"angleDeg":      5,
			})
			res := body["result"].(map[string]any)
			if _, present := res["effectiveGm"]; present {
				errCh <- "无舱档案不应返回自由液面修正字段"
			}
			if math.Abs(res["gm"].(float64)-4.05) > 1e-9 {
				errCh <- fmt.Sprintf("预置驳船 GM 被污染: %v", res["gm"])
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}

// 通过 HTTP 并发打不同名字与同名的计算，验证服务层并发隔离。
func TestHTTP_ConcurrentConditionIsolation(t *testing.T) {
	r, _ := newTestRouter(t)
	const n = 40
	var wg sync.WaitGroup
	errCh := make(chan string, 3*n)

	// 先建 n 条 KG 各异的档
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("vessel-%02d", i)
		code, _ := doJSON(t, r, http.MethodPut, "/api/v1/conditions/"+name, map[string]any{
			"displacementVolume": 1000,
			"kb":                 1.2,
			"kg":                 1.0 + float64(i)*0.05,
			"transverseInertia":  5000,
			"waterDensity":       1025,
		})
		if code != http.StatusOK {
			t.Fatalf("建档 %s 失败: %d", name, code)
		}
	}

	client := func(name string, wantGM float64) {
		defer wg.Done()
		code, body := doJSON(t, r, http.MethodPost, "/api/v1/stability/evaluate", map[string]any{
			"conditionName": name,
			"angleDeg":      5,
		})
		if code != http.StatusOK {
			errCh <- fmt.Sprintf("%s 状态码 %d", name, code)
			return
		}
		gm := body["result"].(map[string]any)["gm"].(float64)
		if math.Abs(gm-wantGM) > 1e-9 {
			errCh <- fmt.Sprintf("%s 的 GM=%v 串到了别的船（期望 %v）", name, gm, wantGM)
		}
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("vessel-%02d", i)
		wantGM := 1.2 + 5.0 - (1.0 + float64(i)*0.05)
		wg.Add(1)
		go client(name, wantGM)
	}
	// 同名档被并发重算：每次都应拿到同一份参数。
	for i := 0; i < n; i++ {
		wg.Add(1)
		go client("rectangular-barge", 4.05)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}
