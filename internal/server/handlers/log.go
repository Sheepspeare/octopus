package handlers

import (
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/bestruirui/octopus/internal/relay"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/gin-contrib/sse"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/log").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("/overview/stream", http.MethodGet).
				Handle(streamOverview),
		).
		AddRoute(
			router.NewRoute("/request-body/:id", http.MethodGet).
				Handle(getRequestBody),
		).
		AddRoute(
			router.NewRoute("/response-body/:id", http.MethodGet).
				Handle(getResponseBody),
		).
		AddRoute(
			router.NewRoute("/list", http.MethodGet).
				Handle(listLogs),
		).
		AddRoute(
			router.NewRoute("/stop/:id", http.MethodPost).
				Handle(stopRequest),
		).
		AddRoute(
			router.NewRoute("/stop/:id/:round", http.MethodPost).
				Handle(stopRequest),
		).
		AddRoute(
			router.NewRoute("/clear", http.MethodDelete).
				Handle(clearLog),
		)
}

// stopRequest 按是否提供轮次参数, 中止单个轮次或整个请求。
func stopRequest(c *gin.Context) {
	requestID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, "invalid request id")
		return
	}
	if roundText := c.Param("round"); roundText != "" {
		round, err := strconv.Atoi(roundText)
		if err != nil || round < 1 {
			resp.Error(c, http.StatusBadRequest, "invalid round")
			return
		}
		relay.Interrupt(requestID, round)
	} else {
		relay.CancelRequest(requestID)
	}
	c.Status(http.StatusNoContent)
}

// clearLog 删除全部已完成的请求记录, 同时清掉落盘的历史与正文文件, 并在释放记录引用后主动执行垃圾回收。
func clearLog(c *gin.Context) {
	relay.Clear()
	runtime.GC()
	c.Status(http.StatusNoContent)
}

// listLogs 按 ID 倒序翻页读取已落库的历史, 供日志页加载内存快照之外更早的记录。
// before_id 传当前列表里最小的 ID 即可续上一页; 不传则从最新一条开始。
func listLogs(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var before uint64
	if text := c.Query("before_id"); text != "" {
		before, err = strconv.ParseUint(text, 10, 64)
		if err != nil {
			resp.Error(c, http.StatusBadRequest, "invalid before_id")
			return
		}
	}

	records, err := relay.ListPersisted(before, limit)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	states := make([]relay.RequestState, 0, len(records))
	for _, record := range records {
		states = append(states, relay.StateOf(record))
	}
	resp.Success(c, states)
}

// getRequestBody 返回指定请求的原始请求体。
// getRequestBody 返回指定请求的请求体; 记录不存在时返回 404, 便于前端区分"暂无"与"为空"。
func getRequestBody(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, "invalid request id")
		return
	}
	body, ok := relay.RequestBody(id)
	if !ok {
		resp.Error(c, http.StatusNotFound, "request body not available")
		return
	}
	resp.Success(c, body)
}

// getResponseBody 返回指定请求当前保存的响应体; 记录不存在时返回 404。
func getResponseBody(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, "invalid request id")
		return
	}
	body, ok := relay.ResponseBody(id)
	if !ok {
		resp.Error(c, http.StatusNotFound, "response body not available")
		return
	}
	resp.Success(c, body)
}

// streamOverview 逐条发送建立连接时的概览及后续请求更新。
func streamOverview(c *gin.Context) {
	prepareSSE(c)
	snapshot, updates := relay.OpenRequestStream()
	defer relay.CloseRequestStream(updates)
	if len(snapshot) == 0 {
		// Flush a real SSE comment so proxies forward the empty-state response immediately.
		if _, err := c.Writer.Write([]byte(": connected\n\n")); err != nil {
			return
		}
		c.Writer.Flush()
	}
	for _, request := range snapshot {
		if err := sse.Encode(c.Writer, sse.Event{Event: "log", Data: request}); err != nil {
			return
		}
		c.Writer.Flush()
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := c.Writer.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			c.Writer.Flush()
		case request, ok := <-updates:
			if !ok {
				return
			}
			if err := sse.Encode(c.Writer, sse.Event{Event: "log", Data: request}); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

// prepareSSE 设置实时日志连接需要的响应头。
func prepareSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}
