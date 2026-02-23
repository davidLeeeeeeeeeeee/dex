package handlers

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	rates := map[string]float64{
		"/test_limit": 10.0, // 10 RPS
		// "/test_no_limit" is implicitly no limit
	}

	rl := NewRateLimiter(rates)

	// Mock handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	limitHandler := rl.LimitMiddleware("/test_limit", handler, nil)
	noLimitHandler := rl.LimitMiddleware("/test_no_limit", handler, nil)

	t.Run("NoLimit", func(t *testing.T) {
		successCount := 0
		for i := 0; i < 50; i++ {
			req, _ := http.NewRequest("GET", "/test_no_limit", nil)
			w := httptest.NewRecorder()
			noLimitHandler.ServeHTTP(w, req)
			if w.Code == http.StatusOK {
				successCount++
			}
		}
		if successCount != 50 {
			t.Errorf("Expected 50 successes for no limit, got %d", successCount)
		}
	})

	t.Run("WithLimit", func(t *testing.T) {
		successCount := 0
		tooManyCount := 0

		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				req, _ := http.NewRequest("GET", "/test_limit", nil)
				w := httptest.NewRecorder()
				limitHandler.ServeHTTP(w, req)

				// Because this happens extremely fast, we expect about 10 successes and 10 TooManyRequests
				// depending on the exact burst mechanics. Rate limit 10 RPS means burst is 10.
				if w.Code == http.StatusOK {
					successCount++
				} else if w.Code == http.StatusTooManyRequests {
					tooManyCount++
				}
			}()
		}
		wg.Wait()

		// Allow slight variance but we should definitely have seen some 429s.
		if successCount > 10 { // burst is 10
			t.Errorf("Expected at most 10 successes for burst, got %d", successCount)
		}
		if tooManyCount < 10 {
			t.Errorf("Expected at least 10 429s, got %d", tooManyCount)
		}
	})

	// Wait a bit to replenish tokens
	time.Sleep(200 * time.Millisecond)

	t.Run("AfterReplenish", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/test_limit", nil)
		w := httptest.NewRecorder()
		limitHandler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 after replenish, got %d", w.Code)
		}
	})
}
