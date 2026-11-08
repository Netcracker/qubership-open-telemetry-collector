package graylog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func closeListener(t *testing.T, listener net.Listener) {
	t.Helper()

	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("failed to close listener: %v", err)
	}
}

func startTCPPayloadServer(t *testing.T, listener net.Listener) (<-chan map[string]any, <-chan struct{}) {
	t.Helper()

	payloadCh := make(chan map[string]any, 1)
	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() {
			if closeErr := conn.Close(); closeErr != nil {
				t.Errorf("failed to close accepted connection: %v", closeErr)
			}
		}()

		payload, err := readPayload(conn)
		if err != nil {
			t.Errorf("failed to read payload: %v", err)
			return
		}
		payloadCh <- payload
	}()

	return payloadCh, serverDone
}

func readPayload(conn net.Conn) (map[string]any, error) {
	data, err := bufio.NewReader(conn).ReadBytes(0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	var payload map[string]any
	if err := json.Unmarshal(data[:len(data)-1], &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func waitForPayload(t *testing.T, payloadCh <-chan map[string]any) map[string]any {
	t.Helper()

	select {
	case payload := <-payloadCh:
		return payload
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for payload")
		return nil
	}
}

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

func TestPrepareMessageEncodesExtraStringsAsJSON(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "null byte", value: "before\x00after", want: "before\x00after"},
		{name: "vertical tab", value: "before\vafter", want: "before\vafter"},
		{name: "invalid UTF-8", value: string([]byte{0xff, 'a'}), want: "\ufffda"},
		{name: "Unicode", value: "\u041f\u0440\u0438\u0432\u0435\u0442", want: "\u041f\u0440\u0438\u0432\u0435\u0442"},
		{name: "quotes and backslashes", value: `quote " and slash \`, want: `quote " and slash \`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := prepareMessage(&Message{
				Version:      "1.1",
				Host:         "host-a",
				ShortMessage: "hello",
				Extra:        map[string]string{"value": tt.value},
			})
			if err != nil {
				t.Fatalf("prepareMessage returned error: %v", err)
			}
			if len(data) == 0 || data[len(data)-1] != 0 {
				t.Fatalf("expected GELF payload to end with null byte, got %v", data)
			}

			var payload map[string]json.RawMessage
			if err := json.Unmarshal(data[:len(data)-1], &payload); err != nil {
				t.Fatalf("failed to decode payload: %v", err)
			}
			var got string
			if err := json.Unmarshal(payload["_value"], &got); err != nil {
				t.Fatalf("failed to decode extra value: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected extra value %q, got %q", tt.want, got)
			}
		})
	}
}

func TestPrepareMessageRejectsNil(t *testing.T) {
	if _, err := prepareMessage(nil); err == nil {
		t.Fatal("expected error for nil message")
	}
}

func TestPrepareMessagePreservesLargeTimestamp(t *testing.T) {
	data, err := prepareMessage(&Message{
		Version:      "1.1",
		Host:         "host-a",
		ShortMessage: "hello",
		Timestamp:    9223372036854775807,
	})
	if err != nil {
		t.Fatalf("prepareMessage returned error: %v", err)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data[:len(data)-1], &payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}
	if got := string(payload["timestamp"]); got != "9223372036854775807" {
		t.Fatalf("expected timestamp to retain int64 precision, got %s", got)
	}
}

func TestAddExtraFieldsRejectsInvalidJSON(t *testing.T) {
	if _, err := addExtraFields([]byte("{"), nil); err == nil {
		t.Fatal("expected error for invalid JSON")
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

func TestNewGraylogSenderSendsMessageOverTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create TCP listener: %v", err)
	}
	defer closeListener(t, listener)

	payloadCh, serverDone := startTCPPayloadServer(t, listener)

	tcpAddr := listener.Addr().(*net.TCPAddr)
	logPath := filepath.Join(t.TempDir(), "sender.jsonl")
	loggerConfig := zap.NewProductionConfig()
	loggerConfig.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	loggerConfig.OutputPaths = []string{logPath}
	logger, err := loggerConfig.Build()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := logger.Sync(); err != nil {
			t.Errorf("flush JSON log: %v", err)
		}
	}()
	sender := NewGraylogSender(
		Endpoint{Transport: TCP, Address: tcpAddr.IP.String(), Port: uint(tcpAddr.Port)},
		logger,
		1,
		1,
		1,
		1,
		time.Millisecond,
	)
	defer sender.Stop()

	if err := sender.SendToQueue(&Message{
		Version:      "1.1",
		Host:         "frontend",
		ShortMessage: "hello tcp",
		FullMessage:  "Bearer secret-token",
		Extra: map[string]string{
			"trace_id": "abc123",
		},
	}); err != nil {
		t.Fatalf("SendToQueue returned error: %v", err)
	}

	payload := waitForPayload(t, payloadCh)
	if payload["short_message"] != "hello tcp" {
		t.Fatalf("unexpected short_message: %#v", payload)
	}
	if payload["_trace_id"] != "abc123" {
		t.Fatalf("unexpected trace_id: %#v", payload)
	}

	closeListener(t, listener)
	<-serverDone
	if err := logger.Sync(); err != nil {
		t.Fatal(err)
	}
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logs), "secret-token") {
		t.Fatalf("payload leaked to logs: %s", logs)
	}
	var sending map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(logs)), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if entry["msg"] == "Sending message" {
			sending = entry
		}
	}
	if sending == nil || sending["payload_bytes"] == nil || sending["goroutine_number"] != float64(0) {
		t.Fatalf("missing structured send record: %s", logs)
	}

}

func TestConnectionLifecycleLogsCarryGoroutineContext(t *testing.T) {
	for _, scenario := range []string{"canceled", "closed queue", "nil message", "connection failure"} {
		t.Run(scenario, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer closeListener(t, listener)
			addr := listener.Addr().(*net.TCPAddr)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sender := &GraylogSender{ctx: ctx, cancel: cancel, endpoint: Endpoint{Transport: TCP, Address: "127.0.0.1", Port: uint(addr.Port)}, msgQueue: make(chan *Message, 1)}
			want := "msgQueue closed, stopping goroutine"
			switch scenario {
			case "canceled":
				cancel()
				want = "Context canceled, stopping goroutine"
			case "nil message":
				sender.msgQueue <- nil
				close(sender.msgQueue)
				want = "nil message received, skipping"
			case "closed queue":
				close(sender.msgQueue)
			case "connection failure":
				sender.endpoint.Transport = "invalid"
				want = "Error creating TCP connection to Graylog"
			}
			var output bytes.Buffer
			config := zap.NewProductionEncoderConfig()
			sender.logger = zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(config), zapcore.Lock(zapcore.AddSync(&output)), zap.DebugLevel), zap.Hooks(func(entry zapcore.Entry) error {
				if entry.Message == "Error creating TCP connection to Graylog" {
					cancel()
				}
				return nil
			}))
			sender.tcpConnGoroutine(7)
			var found bool
			for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
				var entry map[string]any
				if err := json.Unmarshal(line, &entry); err != nil {
					t.Fatalf("invalid JSON: %v", err)
				}
				if entry["msg"] == want {
					found = true
					if entry["goroutine_number"] != float64(7) || entry["logger"] != "graylogtcpconnection" {
						t.Fatalf("missing connection context: %s", line)
					}
				}
			}
			if !found {
				t.Fatalf("missing %q record: %s", want, output.String())
			}
		})
	}
}

func TestRecoveredPanicHasOneStacktraceField(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	finished := make(chan struct{}, 2)
	sender := &GraylogSender{ctx: ctx, cancel: cancel, msgQueue: make(chan *Message)}
	sender.logger = zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.Lock(zapcore.AddSync(&output)), zap.DebugLevel), zap.AddStacktrace(zap.ErrorLevel), zap.Hooks(func(entry zapcore.Entry) error {
		switch entry.Message {
		case "Creating TCP connection to Graylog":
			panic("connection failure")
		case "Restarting goroutine":
			cancel()
		case "Goroutine finished":
			finished <- struct{}{}
		}
		return nil
	}))
	sender.tcpConnGoroutine(3)
	for range 2 {
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("sender goroutine did not finish")
		}
	}
	var found bool
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if entry["msg"] == "Panic in goroutine" {
			found = true
			if bytes.Count(line, []byte(`"stacktrace":`)) != 1 {
				t.Fatalf("expected one stacktrace key: %s", line)
			}
			if !strings.Contains(entry["stacktrace"].(string), "tcpConnGoroutine") {
				t.Fatalf("missing recovered stack: %s", line)
			}
		}
	}
	if !found {
		t.Fatalf("missing recovered panic record: %s", output.String())
	}
}
