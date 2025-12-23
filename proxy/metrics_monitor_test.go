package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/napmany/llmsnap/event"
	"github.com/stretchr/testify/assert"
)

func TestMetricsMonitor_AddMetrics(t *testing.T) {
	t.Run("adds metrics and assigns ID", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		metric := TokenMetrics{
			Model:        "test-model",
			InputTokens:  100,
			OutputTokens: 50,
		}

		mm.addMetrics(metric)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, 0, metrics[0].ID)
		assert.Equal(t, "test-model", metrics[0].Model)
		assert.Equal(t, 100, metrics[0].InputTokens)
		assert.Equal(t, 50, metrics[0].OutputTokens)
	})

	t.Run("increments ID for each metric", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		for i := 0; i < 5; i++ {
			mm.addMetrics(TokenMetrics{Model: "model"})
		}

		metrics := mm.getMetrics()
		assert.Equal(t, 5, len(metrics))
		for i := 0; i < 5; i++ {
			assert.Equal(t, i, metrics[i].ID)
		}
	})

	t.Run("respects max metrics limit", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 3)

		// Add 5 metrics
		for i := 0; i < 5; i++ {
			mm.addMetrics(TokenMetrics{
				Model:       "model",
				InputTokens: i,
			})
		}

		metrics := mm.getMetrics()
		assert.Equal(t, 3, len(metrics))

		// Should keep the last 3 metrics (IDs 2, 3, 4)
		assert.Equal(t, 2, metrics[0].ID)
		assert.Equal(t, 3, metrics[1].ID)
		assert.Equal(t, 4, metrics[2].ID)
	})

	t.Run("emits TokenMetricsEvent", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		receivedEvent := make(chan TokenMetricsEvent, 1)
		cancel := event.On(func(e TokenMetricsEvent) {
			receivedEvent <- e
		})
		defer cancel()

		metric := TokenMetrics{
			Model:        "test-model",
			InputTokens:  100,
			OutputTokens: 50,
		}

		mm.addMetrics(metric)

		select {
		case evt := <-receivedEvent:
			assert.Equal(t, 0, evt.Metrics.ID)
			assert.Equal(t, "test-model", evt.Metrics.Model)
			assert.Equal(t, 100, evt.Metrics.InputTokens)
			assert.Equal(t, 50, evt.Metrics.OutputTokens)
		case <-time.After(1 * time.Second):
			t.Fatal("timeout waiting for event")
		}
	})
}

func TestMetricsMonitor_GetMetrics(t *testing.T) {
	t.Run("returns empty slice when no metrics", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)
		metrics := mm.getMetrics()
		assert.NotNil(t, metrics)
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("returns copy of metrics", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)
		mm.addMetrics(TokenMetrics{Model: "model1"})
		mm.addMetrics(TokenMetrics{Model: "model2"})

		metrics1 := mm.getMetrics()
		metrics2 := mm.getMetrics()

		// Verify we got copies
		assert.Equal(t, 2, len(metrics1))
		assert.Equal(t, 2, len(metrics2))

		// Modify the returned slice shouldn't affect the original
		metrics1[0].Model = "modified"
		metrics3 := mm.getMetrics()
		assert.Equal(t, "model1", metrics3[0].Model)
	})
}

func TestMetricsMonitor_GetMetricsJSON(t *testing.T) {
	t.Run("returns valid JSON for empty metrics", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)
		jsonData, err := mm.getMetricsJSON()
		assert.NoError(t, err)
		assert.NotNil(t, jsonData)

		var metrics []TokenMetrics
		err = json.Unmarshal(jsonData, &metrics)
		assert.NoError(t, err)
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("returns valid JSON with metrics", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)
		mm.addMetrics(TokenMetrics{
			Model:           "model1",
			InputTokens:     100,
			OutputTokens:    50,
			TokensPerSecond: 25.5,
		})
		mm.addMetrics(TokenMetrics{
			Model:           "model2",
			InputTokens:     200,
			OutputTokens:    100,
			TokensPerSecond: 30.0,
		})

		jsonData, err := mm.getMetricsJSON()
		assert.NoError(t, err)

		var metrics []TokenMetrics
		err = json.Unmarshal(jsonData, &metrics)
		assert.NoError(t, err)
		assert.Equal(t, 2, len(metrics))
		assert.Equal(t, "model1", metrics[0].Model)
		assert.Equal(t, "model2", metrics[1].Model)
	})
}

func TestMetricsMonitor_WrapHandler(t *testing.T) {
	t.Run("successful non-streaming request with usage data", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		responseBody := `{
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 50
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, "test-model", metrics[0].Model)
		assert.Equal(t, 100, metrics[0].InputTokens)
		assert.Equal(t, 50, metrics[0].OutputTokens)
	})

	t.Run("successful request with timings data", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		responseBody := `{
			"timings": {
				"prompt_n": 100,
				"predicted_n": 50,
				"prompt_per_second": 150.5,
				"predicted_per_second": 25.5,
				"prompt_ms": 500.0,
				"predicted_ms": 1500.0,
				"cache_n": 20
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, "test-model", metrics[0].Model)
		assert.Equal(t, 100, metrics[0].InputTokens)
		assert.Equal(t, 50, metrics[0].OutputTokens)
		assert.Equal(t, 20, metrics[0].CachedTokens)
		assert.Equal(t, 150.5, metrics[0].PromptPerSecond)
		assert.Equal(t, 25.5, metrics[0].TokensPerSecond)
		assert.Equal(t, 2000, metrics[0].DurationMs) // 500 + 1500
	})

	t.Run("streaming request with SSE format", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// Note: SSE format requires proper line breaks - each data line followed by blank line
		responseBody := `data: {"choices":[{"text":"Hello"}]}

data: {"choices":[{"text":" World"}]}

data: {"usage":{"prompt_tokens":10,"completion_tokens":20},"timings":{"prompt_n":10,"predicted_n":20,"prompt_per_second":100.0,"predicted_per_second":50.0,"prompt_ms":100.0,"predicted_ms":400.0}}

data: [DONE]

`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, "test-model", metrics[0].Model)
		// When timings data is present, it takes precedence
		assert.Equal(t, 10, metrics[0].InputTokens)
		assert.Equal(t, 20, metrics[0].OutputTokens)
	})

	t.Run("non-OK status code does not record metrics", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("error"))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("empty response body does not record metrics", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.WriteHeader(http.StatusOK)
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("invalid JSON does not record metrics", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not valid json"))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err) // Errors after response is sent are logged, not returned

		metrics := mm.getMetrics()
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("next handler error is propagated", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		expectedErr := assert.AnError
		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			return expectedErr
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.Equal(t, expectedErr, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("response without usage or timings records metrics with unknown values", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		responseBody := `{"result": "ok"}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err) // Errors after response is sent are logged, not returned

		// With graceful degradation, should track the request even without usage data
		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, "test-model", metrics[0].Model)
		assert.Equal(t, 0, metrics[0].InputTokens)
		assert.Equal(t, 0, metrics[0].OutputTokens)
		assert.Equal(t, -1, metrics[0].CachedTokens)
	})
}

func TestMetricsMonitor_ResponseBodyCopier(t *testing.T) {
	t.Run("captures response body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		copier := newBodyCopier(ginCtx.Writer, time.Now())

		testData := []byte("test response body")
		n, err := copier.Write(testData)

		assert.NoError(t, err)
		assert.Equal(t, len(testData), n)
		assert.Equal(t, testData, copier.body.Bytes())
		assert.Equal(t, string(testData), rec.Body.String())
	})

	t.Run("sets start time on first write", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		copier := newBodyCopier(ginCtx.Writer, time.Now())

		assert.True(t, copier.StartTime().IsZero())

		copier.Write([]byte("test"))

		assert.False(t, copier.StartTime().IsZero())
	})

	t.Run("preserves headers", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		copier := newBodyCopier(ginCtx.Writer, time.Now())

		copier.Header().Set("X-Test", "value")

		assert.Equal(t, "value", rec.Header().Get("X-Test"))
	})

	t.Run("preserves status code", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		copier := newBodyCopier(ginCtx.Writer, time.Now())

		copier.WriteHeader(http.StatusCreated)

		// Gin's ResponseWriter tracks status internally
		assert.Equal(t, http.StatusCreated, copier.Status())
	})

	t.Run("tracks request start time", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		requestTime := time.Now()
		copier := newBodyCopier(ginCtx.Writer, requestTime)

		assert.Equal(t, requestTime, copier.RequestTime())
	})
}

func TestMetricsMonitor_Concurrent(t *testing.T) {
	t.Run("concurrent addMetrics is safe", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 1000)

		var wg sync.WaitGroup
		numGoroutines := 10
		metricsPerGoroutine := 100

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < metricsPerGoroutine; j++ {
					mm.addMetrics(TokenMetrics{
						Model:        "test-model",
						InputTokens:  id*1000 + j,
						OutputTokens: j,
					})
				}
			}(i)
		}

		wg.Wait()

		metrics := mm.getMetrics()
		assert.Equal(t, numGoroutines*metricsPerGoroutine, len(metrics))
	})

	t.Run("concurrent reads and writes are safe", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 100)

		done := make(chan bool)

		// Writer goroutine
		go func() {
			for i := 0; i < 50; i++ {
				mm.addMetrics(TokenMetrics{Model: "test-model"})
				time.Sleep(1 * time.Millisecond)
			}
			done <- true
		}()

		// Multiple reader goroutines
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					_ = mm.getMetrics()
					_, _ = mm.getMetricsJSON()
					time.Sleep(2 * time.Millisecond)
				}
			}()
		}

		<-done
		wg.Wait()

		// Final check
		metrics := mm.getMetrics()
		assert.Equal(t, 50, len(metrics))
	})
}

func TestMetricsMonitor_ParseMetrics(t *testing.T) {
	t.Run("prefers timings over usage data", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// Timings should take precedence over usage
		responseBody := `{
			"usage": {
				"prompt_tokens": 50,
				"completion_tokens": 25
			},
			"timings": {
				"prompt_n": 100,
				"predicted_n": 50,
				"prompt_per_second": 150.5,
				"predicted_per_second": 25.5,
				"prompt_ms": 500.0,
				"predicted_ms": 1500.0
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		// Should use timings values, not usage values
		assert.Equal(t, 100, metrics[0].InputTokens)
		assert.Equal(t, 50, metrics[0].OutputTokens)
	})

	t.Run("handles missing cache_n in timings", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		responseBody := `{
			"timings": {
				"prompt_n": 100,
				"predicted_n": 50,
				"prompt_per_second": 150.5,
				"predicted_per_second": 25.5,
				"prompt_ms": 500.0,
				"predicted_ms": 1500.0
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, -1, metrics[0].CachedTokens) // Default value when not present
	})

	t.Run("calculates TokensPerSecond when timings absent", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// vLLM-style response: only usage, no timings
		responseBody := `{
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 20
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Start writing, then sleep to simulate streaming duration
			w.Write([]byte(responseBody[:20]))
			time.Sleep(2000 * time.Millisecond)
			w.Write([]byte(responseBody[20:]))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, 10, metrics[0].InputTokens)
		assert.Equal(t, 20, metrics[0].OutputTokens)
		// Should calculate speed: 20 tokens / 2 seconds = ~10 tokens/sec
		// Allow some variance due to timing precision
		assert.Greater(t, metrics[0].TokensPerSecond, 8.0)
		assert.Less(t, metrics[0].TokensPerSecond, 12.0)
		// PromptPerSecond should remain unknown
		assert.Equal(t, -1.0, metrics[0].PromptPerSecond)
	})

	t.Run("prefers backend timings over calculation", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// Response with both usage and timings
		// Timings should be used even if they differ from calculated values
		responseBody := `{
			"usage": {
				"prompt_tokens": 50,
				"completion_tokens": 25
			},
			"timings": {
				"prompt_n": 50,
				"predicted_n": 25,
				"prompt_per_second": 150.5,
				"predicted_per_second": 25.5,
				"prompt_ms": 500.0,
				"predicted_ms": 1000.0
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Simulate different duration than timings reports
			time.Sleep(2000 * time.Millisecond)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		// Should use backend timings, NOT calculated values
		assert.Equal(t, 25.5, metrics[0].TokensPerSecond)
		assert.Equal(t, 150.5, metrics[0].PromptPerSecond)
		assert.Equal(t, 1500, metrics[0].DurationMs) // 500 + 1000 from timings
	})

	t.Run("handles zero output tokens", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// Response with no completion tokens
		responseBody := `{
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 0
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			time.Sleep(100 * time.Millisecond)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, 10, metrics[0].InputTokens)
		assert.Equal(t, 0, metrics[0].OutputTokens)
		// TokensPerSecond should remain -1 (unknown), not divide by zero
		assert.Equal(t, -1.0, metrics[0].TokensPerSecond)
	})

	t.Run("handles very fast responses", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// Response that completes very quickly (< 1ms)
		responseBody := `{
			"usage": {
				"prompt_tokens": 5,
				"completion_tokens": 2
			}
		}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// No sleep - respond immediately
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, 5, metrics[0].InputTokens)
		assert.Equal(t, 2, metrics[0].OutputTokens)
		// Should handle very fast responses without panicking
		// Speed may be very high but should be calculated if duration > 0
		if metrics[0].DurationMs > 0 {
			assert.Greater(t, metrics[0].TokensPerSecond, 0.0)
		} else {
			// If duration is exactly 0, speed stays -1
			assert.Equal(t, -1.0, metrics[0].TokensPerSecond)
		}
	})
}

func TestMetricsMonitor_StreamingResponse(t *testing.T) {
	t.Run("finds metrics in last valid SSE data", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// Metrics should be found in the last data line before [DONE]
		responseBody := `data: {"choices":[{"text":"First"}]}

data: {"choices":[{"text":"Second"}]}

data: {"usage":{"prompt_tokens":100,"completion_tokens":50}}

data: [DONE]

`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, 100, metrics[0].InputTokens)
		assert.Equal(t, 50, metrics[0].OutputTokens)
	})

	t.Run("handles streaming with no valid JSON", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		responseBody := `data: not json

data: [DONE]

`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err) // Errors after response is sent are logged, not returned

		metrics := mm.getMetrics()
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("handles empty streaming response", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		responseBody := ``

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		// Empty body should not trigger WrapHandler processing
		assert.NoError(t, err)

		metrics := mm.getMetrics()
		assert.Equal(t, 0, len(metrics))
	})

	t.Run("gracefully handles streaming response without usage data", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// vLLM streaming response without stream_options - no usage data
		responseBody := `data: {"choices":[{"text":"Hello"}]}

data: {"choices":[{"text":" world"}]}

data: [DONE]

`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		// Should track the request even without usage data
		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, "test-model", metrics[0].Model)
		// Unknown values should be 0 or -1
		assert.Equal(t, 0, metrics[0].InputTokens)
		assert.Equal(t, 0, metrics[0].OutputTokens)
		assert.Equal(t, -1, metrics[0].CachedTokens)
		assert.Equal(t, -1.0, metrics[0].PromptPerSecond)
		assert.Equal(t, -1.0, metrics[0].TokensPerSecond)
	})

	t.Run("gracefully handles non-streaming response without usage data", func(t *testing.T) {
		mm := newMetricsMonitor(testLogger, 10)

		// Valid JSON response but no usage or timings fields
		responseBody := `{"choices":[{"text":"Hello world"}],"model":"test-model"}`

		nextHandler := func(modelID string, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(responseBody))
			return nil
		}

		req := httptest.NewRequest("POST", "/test", nil)
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)

		err := mm.wrapHandler("test-model", ginCtx.Writer, req, nextHandler)
		assert.NoError(t, err)

		// Should track the request even without usage data
		metrics := mm.getMetrics()
		assert.Equal(t, 1, len(metrics))
		assert.Equal(t, "test-model", metrics[0].Model)
		// Unknown values should be 0 or -1
		assert.Equal(t, 0, metrics[0].InputTokens)
		assert.Equal(t, 0, metrics[0].OutputTokens)
		assert.Equal(t, -1, metrics[0].CachedTokens)
		assert.Equal(t, -1.0, metrics[0].PromptPerSecond)
		assert.Equal(t, -1.0, metrics[0].TokensPerSecond)
	})
}

// Benchmark tests
func BenchmarkMetricsMonitor_AddMetrics(b *testing.B) {
	mm := newMetricsMonitor(testLogger, 1000)

	metric := TokenMetrics{
		Model:           "test-model",
		CachedTokens:    100,
		InputTokens:     500,
		OutputTokens:    250,
		PromptPerSecond: 1200.5,
		TokensPerSecond: 45.8,
		DurationMs:      5000,
		Timestamp:       time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mm.addMetrics(metric)
	}
}

func BenchmarkMetricsMonitor_AddMetrics_SmallBuffer(b *testing.B) {
	// Test performance with a smaller buffer where wrapping occurs more frequently
	mm := newMetricsMonitor(testLogger, 100)

	metric := TokenMetrics{
		Model:           "test-model",
		CachedTokens:    100,
		InputTokens:     500,
		OutputTokens:    250,
		PromptPerSecond: 1200.5,
		TokensPerSecond: 45.8,
		DurationMs:      5000,
		Timestamp:       time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mm.addMetrics(metric)
	}
}
