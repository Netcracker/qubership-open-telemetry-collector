package logtcpexporter

import (
	"testing"

	"go.uber.org/zap"
)

func TestConfigValidate(t *testing.T) {
	cfg := &Config{
		ConnPoolSize:                1,
		QueueSize:                   1,
		MaxMessageSendRetryCnt:      0,
		MaxSuccessiveSendErrCnt:     0,
		SuccessiveSendErrFreezeTime: "1s",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}

	cfg.ConnPoolSize = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected connection-pool-size validation error")
	}
}

func TestSmallHelpers(t *testing.T) {
	if got := getFirst("", "a", "b"); got != "a" {
		t.Fatalf("unexpected getFirst result: %q", got)
	}
	if got := getFirst("", ""); got != "" {
		t.Fatalf("unexpected getFirst empty result: %q", got)
	}
	if got := getFirstInt64(0, 5, 7); got != 5 {
		t.Fatalf("unexpected getFirstInt64 result: %d", got)
	}

	lte := &logTcpExporter{logger: zap.NewNop()}
	if got := lte.getGraylogLevel("warning"); got != 4 {
		t.Fatalf("unexpected graylog level: %d", got)
	}
	if got := lte.getGraylogLevel("unknown"); got != 3 {
		t.Fatalf("unexpected graylog fallback level: %d", got)
	}
}
