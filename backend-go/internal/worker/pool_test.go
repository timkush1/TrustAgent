package worker

import (
	"testing"
	"time"
)

func TestNewPool(t *testing.T) {
	pool := NewPool(5, 100, nil, nil)

	if pool == nil {
		t.Fatal("NewPool returned nil")
	}
	if pool.workers != 5 {
		t.Errorf("Expected 5 workers, got %d", pool.workers)
	}
	if cap(pool.queue) != 100 {
		t.Errorf("Expected queue capacity 100, got %d", cap(pool.queue))
	}
}

func TestPoolQueueLength(t *testing.T) {
	pool := NewPool(2, 10, nil, nil)

	if pool.QueueLength() != 0 {
		t.Errorf("Expected queue length 0, got %d", pool.QueueLength())
	}
}

func TestAuditJob(t *testing.T) {
	job := &AuditJob{
		RequestID:   "req-123",
		Prompt:      "What is 2+2?",
		Response:    "4",
		Model:       "gpt-4",
		Timestamp:   time.Now(),
		UserID:      "user-1",
		RequestPath: "/v1/chat/completions",
	}

	if job.RequestID != "req-123" {
		t.Errorf("Expected RequestID 'req-123', got '%s'", job.RequestID)
	}
	if job.Model != "gpt-4" {
		t.Errorf("Expected Model 'gpt-4', got '%s'", job.Model)
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"Hello", 10, "Hello"},
		{"Hello World", 8, "Hello..."},
		{"Short", 5, "Short"},
		{"Exactly10!", 10, "Exactly10!"},
		{"TooLong!!!", 6, "Too..."},
	}

	for _, tc := range tests {
		result := truncateString(tc.input, tc.maxLen)
		if result != tc.expected {
			t.Errorf("truncateString(%q, %d) = %q, expected %q",
				tc.input, tc.maxLen, result, tc.expected)
		}
	}
}

func TestPoolSubmitWithNoClient(t *testing.T) {
	pool := NewPool(1, 10, nil, nil)
	go pool.Start()
	defer pool.Stop()

	time.Sleep(50 * time.Millisecond)

	job := &AuditJob{
		RequestID: "test-1",
		Prompt:    "Test prompt",
		Response:  "Test response",
		Model:     "test-model",
		Timestamp: time.Now(),
	}

	pool.Submit(job)
	time.Sleep(100 * time.Millisecond)
}
