package sentryreceiver

import (
	"testing"

	"github.com/Netcracker/qubership-open-telemetry-collector/receiver/sentryreceiver/models"
	"go.uber.org/zap"
)

func TestParseEnvelopEventTransaction(t *testing.T) {
	sr := &sentrytraceReceiver{logger: zap.NewNop()}
	body := "{\"event_id\":\"evt-1\",\"sdk\":{\"name\":\"sdk\",\"version\":\"1.0\"}}\n" +
		"{\"type\":\"transaction\"}\n" +
		"{\"event_id\":\"evt-1\",\"transaction\":\"/orders/1\",\"contexts\":{\"trace\":{\"op\":\"http.client\",\"span_id\":\"0123456789abcdef\",\"trace_id\":\"0123456789abcdef0123456789abcdef\"}},\"timestamp\":123.4,\"start_timestamp\":120.1}\n"

	got, err := sr.ParseEnvelopEvent(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.EnvelopType != models.ENVELOP_TYPE_TRANSACTION {
		t.Fatalf("unexpected envelope type: %d", got.EnvelopType)
	}
	if len(got.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got.Events))
	}
	if got.EventID != "evt-1" {
		t.Fatalf("unexpected event id: %q", got.EventID)
	}
}

func TestParseEnvelopEventSession(t *testing.T) {
	sr := &sentrytraceReceiver{logger: zap.NewNop()}
	body := "{\"event_id\":\"evt-2\"}\n" +
		"{\"type\":\"session\"}\n" +
		"{\"sid\":\"550e8400-e29b-41d4-a716-446655440000\",\"status\":\"exited\",\"timestamp\":\"2025-01-02T03:04:05Z\"}\n"

	got, err := sr.ParseEnvelopEvent(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.EnvelopType != models.ENVELOP_TYPE_SESSION {
		t.Fatalf("unexpected envelope type: %d", got.EnvelopType)
	}
	if len(got.SessionEvents) != 1 {
		t.Fatalf("expected 1 session event, got %d", len(got.SessionEvents))
	}
}

func TestParseEnvelopEventErrors(t *testing.T) {
	sr := &sentrytraceReceiver{logger: zap.NewNop()}

	if _, err := sr.ParseEnvelopEvent("too\nshort"); err == nil {
		t.Fatal("expected error for short envelope")
	}
	if _, err := sr.ParseEnvelopEvent("not-json\n{\"type\":\"event\"}\n{}"); err == nil {
		t.Fatal("expected header unmarshal error")
	}
	if _, err := sr.ParseEnvelopEvent("{}\n{\"type\":\"event\"}\nnot-json"); err == nil {
		t.Fatal("expected payload unmarshal error")
	}
	if _, err := sr.ParseEnvelopEvent("{}\n{\"type\":\"unknown\"}\n{\"x\":1}\n"); err == nil {
		t.Fatal("expected no useful payload error")
	}
}
