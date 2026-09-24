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
