// Package api 装配 HTTP 路由，把请求参数交给稳性内核与档案服务处理。
// 本服务只提供 JSON 接口，不含任何前端页面。
package api

import (
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

// evaluateRequest 单点核算请求：可按已存档名引用，也可内联给出参数。
type evaluateRequest struct {
	ConditionName string         `json:"conditionName"`
	Params        *paramsPayload `json:"params"`
	// AngleDeg 横倾角（度）；省略时按正浮 0° 处理。
	AngleDeg float64 `json:"angleDeg"`
}

// scanRequest 复原力臂曲线扫描请求。
type scanRequest struct {
	ConditionName string         `json:"conditionName"`
	Params        *paramsPayload `json:"params"`
	StartDeg      *float64       `json:"startDeg"`
	EndDeg        *float64       `json:"endDeg"`
	StepDeg       *float64       `json:"stepDeg"`
}

// conditionPayload 建档请求体（POST 时名字在体内，PUT 时名字取自路径）。
type conditionPayload struct {
	Name               string  `json:"name"`
	DisplacementVolume float64 `json:"displacementVolume"`
	KB                 float64 `json:"kb"`
	KG                 float64 `json:"kg"`
	TransverseInertia  float64 `json:"transverseInertia"`
	WaterDensity       float64 `json:"waterDensity"`
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

func fromCondition(c archive.Condition) conditionPayload {
	return conditionPayload{
		Name:               c.Name,
		DisplacementVolume: c.DisplacementVolume,
		KB:                 c.KB,
		KG:                 c.KG,
		TransverseInertia:  c.TransverseInertia,
		WaterDensity:       c.WaterDensity,
	}
}

// resolveParams 在「引用档名」与「内联参数」两种方式中解析出一份内核参数。
// 两者必须二选一；同名引用各自独立取档，计算过程互不渗透。
func (s *Server) resolveParams(c *gin.Context, name string, inline *paramsPayload) (stability.Params, bool) {
	hasName := name != ""
	hasInline := inline != nil
	switch {
	case hasName && hasInline:
		badRequest(c, "conditionName 与 params 只能二选一，不能同时给出")
		return stability.Params{}, false
	case hasName:
		p, err := s.Conditions.ResolveParams(name)
		if err != nil {
			abortByError(c, err)
			return stability.Params{}, false
		}
		return p, true
	case hasInline:
		return toParams(*inline), true
	default:
		badRequest(c, "必须给出 conditionName（引用已存档档名）或 params（内联浮态参数）")
		return stability.Params{}, false
	}
}

// ---- 业务 handler ----

func (s *Server) evaluate(c *gin.Context) {
	var req evaluateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	p, ok := s.resolveParams(c, req.ConditionName, req.Params)
	if !ok {
		return
	}

	result, err := stability.Evaluate(p, req.AngleDeg)
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
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体不是合法 JSON: "+err.Error())
		return
	}
	if req.StartDeg == nil || req.EndDeg == nil || req.StepDeg == nil {
		badRequest(c, "扫描必须显式给出 startDeg、endDeg 与 stepDeg（度）")
		return
	}
	p, ok := s.resolveParams(c, req.ConditionName, req.Params)
	if !ok {
		return
	}

	result, err := stability.Scan(stability.ScanParams{
		Params:   p,
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
	cond := archive.Condition(payload)
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
	cond := archive.Condition(payload)
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
