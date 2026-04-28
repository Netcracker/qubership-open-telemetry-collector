package graylogexporter

import (
	"testing"
	"time"

	"github.com/Netcracker/qubership-open-telemetry-collector/common/graylog"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

func TestGraylogExporterHelpers(t *testing.T) {
	host, port, err := parseEndpoint("graylog:12202")
	if err != nil || host != "graylog" || port != 12202 {
		t.Fatalf("unexpected parsed endpoint: %q %d %v", host, port, err)
	}
	host, port, err = parseEndpoint("graylog")
	if err != nil || port != defaultGraylogPort {
		t.Fatalf("unexpected default endpoint parse: %q %d %v", host, port, err)
	}
	if _, _, err := parseEndpoint("graylog:bad"); err == nil {
		t.Fatal("expected invalid port error")
	}

	if attrs, msg, err := extractAttributes(pcommon.NewValueStr(`{"message":"hello"}{"service":"frontend"}`)); err != nil || msg != "hello" || attrs["service"] != "frontend" {
		t.Fatalf("unexpected string body parsing: %#v %q %v", attrs, msg, err)
	}
	if attrs, msg, err := extractAttributes(pcommon.NewValueStr("plain text")); err != nil || attrs != nil || msg != "plain text" {
		t.Fatalf("unexpected plain text body parsing: %#v %q %v", attrs, msg, err)
	}

	mapVal := pcommon.NewValueMap()
	mapVal.Map().PutStr("message", "mapped")
	mapVal.Map().PutStr("service", "frontend")
	if attrs, msg, err := extractAttributes(mapVal); err != nil || msg != "mapped" || attrs["service"] != "frontend" {
		t.Fatalf("unexpected map body parsing: %#v %q %v", attrs, msg, err)
	}

	bytesVal := pcommon.NewValueBytes()
	bytesVal.Bytes().FromRaw([]byte("raw"))
	if attrs, msg, err := extractAttributes(bytesVal); err != nil || msg != defaultNoMessage || string(attrs["bytes"].([]byte)) != "raw" {
		t.Fatalf("unexpected bytes body parsing: %#v %q %v", attrs, msg, err)
	}

	if _, err := decodeConcatenatedJSON("{bad json}"); err == nil {
		t.Fatal("expected JSON decoding error")
	}

	boolVal := pcommon.NewValueBool(true)
	if got, ok := getStringFromPcommonValue(boolVal); !ok || got != "true" {
		t.Fatalf("unexpected bool conversion: %q %v", got, ok)
	}
	doubleVal := pcommon.NewValueDouble(1.5)
	if got, ok := getStringFromPcommonValue(doubleVal); !ok || got != "1.500000" {
		t.Fatalf("unexpected double conversion: %q %v", got, ok)
	}

	merged := mergeAttributes(map[string]interface{}{"count": 5})
	if merged["count"] != "5" {
		t.Fatalf("unexpected merged attributes: %#v", merged)
	}
	dest := map[string]string{}
	attrMap := pcommon.NewMap()
	attrMap.PutStr("service.name", "frontend")
	attrMap.PutInt("retries", 2)
	mergeMapAttributes(dest, attrMap)
	if dest["service.name"] != "frontend" || dest["retries"] != "2" {
		t.Fatalf("unexpected merged map attributes: %#v", dest)
	}

	if got := defaultIfEmpty("   ", "fallback"); got != "fallback" {
		t.Fatalf("unexpected default fallback: %q", got)
	}
	if got := defaultIfEmpty("message", "fallback"); got != "message" {
		t.Fatalf("unexpected non-empty fallback result: %q", got)
	}
}

func TestLogRecordToMessageAndTimestampLevel(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	cfg.GELFMapping = GELFFieldMapping{
		Version:      defaultGELFVersion,
		Host:         "host",
		ShortMessage: "short-message",
		FullMessage:  "full-message",
		Level:        "level",
	}
	le := createLogExporter(cfg, exporter.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zap.NewNop(),
		},
	})
	le.graylogSender = &graylog.GraylogSender{}

	record := plog.NewLogRecord()
	record.SetTimestamp(pcommon.NewTimestampFromTime(time.Unix(123, 0)))
	record.SetSeverityText("error")
	record.Body().SetStr(`{"short-message":"short","full-message":"full","host":"frontend","message":"hello"}`)
	record.Attributes().PutStr("service.name", "frontend")

	msg, err := le.logRecordToMessage(record, pcommon.NewMap())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.ShortMessage != "short" || msg.FullMessage != "full" || msg.Host != "frontend" {
		t.Fatalf("unexpected mapped message: %#v", msg)
	}

	ts, lvl := le.getTimestampAndLevel(record)
	if ts.Unix() != 123 || lvl != 3 {
		t.Fatalf("unexpected timestamp/level: %v %d", ts, lvl)
	}

	record.SetSeverityText("")
	record.SetSeverityNumber(plog.SeverityNumberWarn2)
	_, lvl = le.getTimestampAndLevel(record)
	if lvl != 4 {
		t.Fatalf("unexpected severity-number mapping: %d", lvl)
	}
}
