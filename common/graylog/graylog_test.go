package graylog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
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

func TestNewGraylogSenderSendsMessageOverTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create TCP listener: %v", err)
	}
	defer closeListener(t, listener)

	payloadCh, serverDone := startTCPPayloadServer(t, listener)

	tcpAddr := listener.Addr().(*net.TCPAddr)
	sender := NewGraylogSender(
		Endpoint{Transport: TCP, Address: tcpAddr.IP.String(), Port: uint(tcpAddr.Port)},
		zap.NewNop(),
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
}
