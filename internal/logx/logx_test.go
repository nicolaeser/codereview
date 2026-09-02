package logx

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestErrorFieldIsStringified(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "error")
	logger.Error("boom", "error", errors.New("missing token"))
	out := buf.String()
	if !strings.Contains(out, `"error":"missing token"`) {
		t.Fatalf("log output = %s", out)
	}
	if strings.Contains(out, `"error":{}`) {
		t.Fatalf("error field was not stringified: %s", out)
	}
}
