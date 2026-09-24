// Package api 装配 HTTP 路由，把请求参数交给稳性内核与档案服务处理。
// 本服务只提供 JSON 接口，不含任何前端页面。
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"shipstability/internal/archive"
	"shipstability/internal/stability"
	"shipstability/internal/store"
)

// Server 持有路由处理所需的依赖。
type Server struct {
	Conditions *archive.Service
}

// NewServer 创建 API 服务。
func NewServer(conds *archive.Service) *Server {
	return &Server{Conditions: conds}
}

// Router 装配全部路由，返回可供测试与 main 使用的 gin 引擎。
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/health", s.health)

	api := r.Group("/api/v1")
	{
		api.POST("/stability/evaluate", s.evaluate)
		api.POST("/stability/scan", s.scan)

		api.GET("/conditions", s.listConditions)
		api.POST("/conditions", s.createCondition)
		api.GET("/conditions/:name", s.getCondition)
		api.PUT("/conditions/:name", s.putCondition)
		api.DELETE("/conditions/:name", s.deleteCondition)
	}
	return r
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "ship-initial-stability"})
}

// ---- 请求 / 响应结构 ----

// paramsPayload 是浮态参数的请求体；水密度省略或为 0 时取默认海水密度。
type paramsPayload struct {
	DisplacementVolume float64 `json:"displacementVolume"`
	KB                 float64 `json:"kb"`
	KG                 float64 `json:"kg"`
	TransverseInertia  float64 `json:"transverseInertia"`
	WaterDensity       float64 `json:"waterDensity"`
}

// tankPayload 是一个液舱的自由液面描述：几何给长宽或直接给惯性矩（二选一），
// 另附舱内液体密度与空舱/部分注液/满舱状态。
type tankPayload struct {
	Name               string  `json:"name"`
	Length             float64 `json:"length"`
	Width              float64 `json:"width"`
	FreeSurfaceInertia float64 `json:"freeSurfaceInertia"`
	LiquidDensity      float64 `json:"liquidDensity"`
	Status             string  `json:"status"`
}

func toTanks(ts []tankPayload) []stability.Tank {
	if ts == nil {
		return nil
	}
	out := make([]stability.Tank, len(ts))
	for i, t := range ts {
		out[i] = stability.Tank{
			Name:               t.Name,
			Length:             t.Length,
			Width:              t.Width,
			FreeSurfaceInertia: t.FreeSurfaceInertia,
			LiquidDensity:      t.LiquidDensity,
			Status:             t.Status,
		}
	}
	return out
}

func fromTanks(ts []stability.Tank) []tankPayload {
	if ts == nil {
		return nil
	}
	out := make([]tankPayload, len(ts))
	for i, t := range ts {
		out[i] = tankPayload{
			Name:               t.Name,
			Length:             t.Length,
			Width:              t.Width,
			FreeSurfaceInertia: t.FreeSurfaceInertia,
			LiquidDensity:      t.LiquidDensity,
			Status:             t.Status,
		}
	}
	return out
}

// evaluateRequest 单点核算请求：可按已存档名引用，也可内联给出参数。
type evaluateRequest struct {
	ConditionName string         `json:"conditionName"`
	Params        *paramsPayload `json:"params"`
	// Tanks 本次核算附带的液舱清单。省略（null/缺省）时：内联参数按
	// 无液舱处理，引用档名时使用档案自带液舱；显式给出空数组 [] 表示
	// 本次不带任何液舱（覆盖档案自带清单）。
	Tanks []tankPayload `json:"tanks"`
	// TanksGiven 标记请求里是否显式带了 tanks 字段（含 []）。
	TanksGiven bool `json:"-"`
	// AngleDeg 横倾角（度）；省略时按正浮 0° 处理。
	AngleDeg float64 `json:"angleDeg"`
}

// scanRequest 复原力臂曲线扫描请求。
type scanRequest struct {
	ConditionName string         `json:"conditionName"`
	Params        *paramsPayload `json:"params"`
	Tanks         []tankPayload  `json:"tanks"`
	TanksGiven    bool           `json:"-"`
	StartDeg      *float64       `json:"startDeg"`
	EndDeg        *float64       `json:"endDeg"`
	StepDeg       *float64       `json:"stepDeg"`
}

// conditionPayload 建档请求体（POST 时名字在体内，PUT 时名字取自路径）。
type conditionPayload struct {
	Name               string        `json:"name"`
	DisplacementVolume float64       `json:"displacementVolume"`
	KB                 float64       `json:"kb"`
	KG                 float64       `json:"kg"`
	TransverseInertia  float64       `json:"transverseInertia"`
	WaterDensity       float64       `json:"waterDensity"`
	Tanks              []tankPayload `json:"tanks"`
}

func toParams(p paramsPayload) stability.Params {
	return stability.Params{
		DisplacementVolume: p.DisplacementVolume,
		KB:                 p.KB,
		KG:                 p.KG,
		TransverseInertia:  p.TransverseInertia,
		WaterDensity:       p.WaterDensity,
	}
}

// unmarshalWithTanksGiven 解析请求体并标出 tanks 字段是否被显式给出
// （显式给空数组 [] 与字段缺省在语义上不同，见 evaluateRequest 注释）。
func unmarshalWithTanksGiven(ctx *gin.Context, req any, setGiven func(bool)) error {
	raw, err := ctx.GetRawData()
	if err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	setGiven(false)
	if v, ok := probe["tanks"]; ok && string(v) != "null" {
		setGiven(true)
	}
	return json.Unmarshal(raw, req)
}

func fromCondition(c archive.Condition) conditionPayload {
	return conditionPayload{
		Name:               c.Name,
		DisplacementVolume: c.DisplacementVolume,
		KB:                 c.KB,
		KG:                 c.KG,
		TransverseInertia:  c.TransverseInertia,
		WaterDensity:       c.WaterDensity,
		Tanks:              fromTanks(c.Tanks),
	}
}

func toCondition(p conditionPayload) archive.Condition {
	return archive.Condition{
		Name:               p.Name,
		DisplacementVolume: p.DisplacementVolume,
		KB:                 p.KB,
		KG:                 p.KG,
		TransverseInertia:  p.TransverseInertia,
		WaterDensity:       p.WaterDensity,
		Tanks:              toTanks(p.Tanks),
	}
}

// resolveInput 在「引用档名」与「内联参数」两种方式中解析出内核参数与
// 本次生效的液舱清单。两者必须二选一；同名引用各自独立取档，计算过程
// 互不渗透。
//
// 液舱来源规则：请求显式携带 tanks（哪怕为空数组）时以请求为准；省略
// tanks 时，引用档名取档案自带液舱，内联参数则无液舱。
func (s *Server) resolveInput(c *gin.Context, name string, inline *paramsPayload, reqTanks []tankPayload, tanksGiven bool) (stability.Params, []stability.Tank, bool) {
	hasName := name != ""
	hasInline := inline != nil
	switch {
	case hasName && hasInline:
		badRequest(c, "conditionName 与 params 只能二选一，不能同时给出")
		return stability.Params{}, nil, false
	case hasName:
		p, storedTanks, err := s.Conditions.Resolve(name)
		if err != nil {
			abortByError(c, err)
			return stability.Params{}, nil, false
		}
		if tanksGiven {
			return p, toTanks(reqTanks), true
		}
		return p, storedTanks, true
	case hasInline:
		return toParams(*inline), toTanks(reqTanks), true
	default:
		badRequest(c, "必须给出 conditionName（引用已存档档名）或 params（内联浮态参数）")
		return stability.Params{}, nil, false
	}
}

// ---- 业务 handler ----

func (s *Server) evaluate(c *gin.Context) {
	var req evaluateRequest
	if err := unmarshalWithTanksGiven(c, &req, func(g bool) { req.TanksGiven = g }); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	p, tanks, ok := s.resolveInput(c, req.ConditionName, req.Params, req.Tanks, req.TanksGiven)
	if !ok {
		return
	}

	result, err := stability.EvaluateLoaded(p, tanks, req.AngleDeg)
	if err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"stability": result.Stability,
		"positive":  result.Stability == stability.StabilityPositive,
		"result":    result,
	})
}

func (s *Server) scan(c *gin.Context) {
	var req scanRequest
	if err := unmarshalWithTanksGiven(c, &req, func(g bool) { req.TanksGiven = g }); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.StartDeg == nil || req.EndDeg == nil || req.StepDeg == nil {
		badRequest(c, "扫描必须显式给出 startDeg、endDeg 与 stepDeg（度）")
		return
	}
	p, tanks, ok := s.resolveInput(c, req.ConditionName, req.Params, req.Tanks, req.TanksGiven)
	if !ok {
		return
	}

	result, err := stability.ScanLoaded(stability.ScanParams{
		Params:   p,
		Tanks:    tanks,
		StartDeg: *req.StartDeg,
		EndDeg:   *req.EndDeg,
		StepDeg:  *req.StepDeg,
	})
	if err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (s *Server) listConditions(c *gin.Context) {
	conds, err := s.Conditions.List()
	if err != nil {
		abortByError(c, err)
		return
	}
	out := make([]conditionPayload, 0, len(conds))
	for _, cond := range conds {
		out = append(out, fromCondition(cond))
	}
	c.JSON(http.StatusOK, gin.H{"conditions": out})
}

func (s *Server) createCondition(c *gin.Context) {
	var payload conditionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	cond := toCondition(payload)
	if err := s.Conditions.Save(cond); err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"condition": fromCondition(cond)})
}

func (s *Server) getCondition(c *gin.Context) {
	cond, err := s.Conditions.Get(c.Param("name"))
	if err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"condition": fromCondition(cond)})
}

// putCondition 按路径名字建档/覆盖（upsert），忽略体内的 name 字段。
func (s *Server) putCondition(c *gin.Context) {
	var payload conditionPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	payload.Name = c.Param("name")
	cond := toCondition(payload)
	if err := s.Conditions.Save(cond); err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"condition": fromCondition(cond)})
}

func (s *Server) deleteCondition(c *gin.Context) {
	if err := s.Conditions.Delete(c.Param("name")); err != nil {
		abortByError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": c.Param("name")})
}

// ---- 错误处理 ----

func badRequest(c *gin.Context, reason string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_parameters", "reason": reason})
}

// abortByError 把领域错误映射成 HTTP 状态：
// 参数/名字非法 → 400，档不存在 → 404，其余 → 500。
func abortByError(c *gin.Context, err error) {
	var ve *stability.ValidationError
	switch {
	case errors.As(err, &ve):
		badRequest(c, ve.Reason)
	case errors.Is(err, store.ErrNotFound):
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found", "reason": err.Error()})
	default:
		// archive 的名字校验等普通参数错误也归 400。
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid_parameters", "reason": err.Error()})
	}
}
