package graylog

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPrepareMessageAddsExtrasAndNullTerminator(t *testing.T) {
	data, err := prepareMessage(&Message{
		Version:      "1.1",
		Host:         "host-a",
		ShortMessage: "hello",
		Extra: map[string]string{
			"trace_id": "abc123",
		},
	})
	if err != nil {
		t.Fatalf("prepareMessage returned error: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != 0 {
		t.Fatalf("expected GELF payload to end with null byte, got %v", data)
	}

	var payload map[string]any
	if err := json.Unmarshal(data[:len(data)-1], &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if payload["_trace_id"] != "abc123" {
		t.Fatalf("unexpected extra fields: %#v", payload)
	}
	if payload["short_message"] != "hello" {
		t.Fatalf("unexpected short_message: %#v", payload)
	}
}

func TestPrepareMessageRejectsNil(t *testing.T) {
	if _, err := prepareMessage(nil); err == nil {
		t.Fatal("expected error for nil message")
	}
}

func TestSendToQueueStates(t *testing.T) {
	logger := zap.NewNop()
	sender := &GraylogSender{
		ctx:      context.Background(),
		msgQueue: make(chan *Message, 1),
		logger:   logger,
	}

	if err := sender.SendToQueue(&Message{ShortMessage: "ok"}); err != nil {
		t.Fatalf("expected queue send to succeed, got %v", err)
	}
	if err := sender.SendToQueue(&Message{ShortMessage: "full"}); err == nil {
		t.Fatal("expected queue full error")
	}

	stoppedCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := &GraylogSender{
		ctx:      stoppedCtx,
		msgQueue: make(chan *Message),
		logger:   logger,
	}
	if err := stopped.SendToQueue(&Message{ShortMessage: "stopped"}); err == nil || err.Error() != "sender stopped" {
		t.Fatalf("expected stopped sender error, got %v", err)
	}
}

func TestNewGraylogSenderStop(t *testing.T) {
	sender := NewGraylogSender(
		Endpoint{Transport: TCP, Address: "127.0.0.1", Port: 12201},
		zap.NewNop(),
		0,
		1,
		1,
		1,
		time.Millisecond,
	)
	sender.Stop()

	select {
	case <-sender.ctx.Done():
	default:
		t.Fatal("expected sender context to be canceled")
	}
}
