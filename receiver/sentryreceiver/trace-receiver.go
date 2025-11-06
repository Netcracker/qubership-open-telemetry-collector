// Copyright 2025 Qubership
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package sentryreceiver

import (
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Netcracker/qubership-open-telemetry-collector/receiver/sentryreceiver/models"
	"github.com/Netcracker/qubership-open-telemetry-collector/utils"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/receiverhelper"
	conventions "go.opentelemetry.io/collector/semconv/v1.9.0"
	"go.uber.org/zap"
)

var errNextConsumerRespBody = []byte(`"Internal Server Error"`)
var errBadRequestRespBody = []byte(`"Bad Request"`)

var timestampSpanDataAttributes = map[string]bool{
	"http.request.redirect_start":          true,
	"http.request.fetch_start":             true,
	"http.request.domain_lookup_start":     true,
	"http.request.domain_lookup_end":       true,
	"http.request.connect_start":           true,
	"http.request.secure_connection_start": true,
	"http.request.connection_end":          true,
	"http.request.request_start":           true,
	"http.request.response_start":          true,
	"http.request.response_end":            true,
}

type sentrytraceReceiver struct {
	host         component.Host
	cancel       context.CancelFunc
	logger       *zap.Logger
	nextConsumer consumer.Traces
	config       *Config

	server     *http.Server
	shutdownWG sync.WaitGroup

	settings receiver.Settings
	obsrecvr *receiverhelper.ObsReport
}

func newReceiver(config *Config, nextConsumer consumer.Traces, settings receiver.Settings) (*sentrytraceReceiver, error) {

	obsrecvr, err := receiverhelper.NewObsReport(receiverhelper.ObsReportSettings{
		ReceiverID:             settings.ID,
		ReceiverCreateSettings: settings,
	})
	if err != nil {
		return nil, err
	}

	sr := &sentrytraceReceiver{
		nextConsumer: nextConsumer,
		config:       config,
		settings:     settings,
		obsrecvr:     obsrecvr,
		logger:       settings.Logger,
	}
	return sr, nil
}
func (sr *sentrytraceReceiver) Start(ctx context.Context, host component.Host) error {
	sr.host = host
	ctx, sr.cancel = context.WithCancel(ctx)

	sr.logger.Info("SentryReceiver started")
	if host == nil {
		return errors.New("nil host")
	}

	var err error
	cfg := sr.config.ServerConfig
	sr.server, err = cfg.ToServer(ctx, host, sr.settings.TelemetrySettings, sr)
	if err != nil {
		return err
	}

	listener, err := cfg.ToListener(ctx)
	if err != nil {
		return err
	}

	sr.shutdownWG.Add(1)
	go func() {
		defer sr.shutdownWG.Done()
		if errHTTP := sr.server.Serve(listener); !errors.Is(errHTTP, http.ErrServerClosed) && errHTTP != nil {
			sr.logger.Sugar().Fatal(errHTTP)
		}
	}()

	return nil
}

func (sr *sentrytraceReceiver) Shutdown(ctx context.Context) error {
	sr.logger.Info("SentryReceiver is shutdown")
	return nil
}
func (sr *sentrytraceReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ctx = sr.obsrecvr.StartTracesOp(ctx)

	w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")

	pr := processBodyIfNecessary(r)
	slurp, readErr := io.ReadAll(pr)
	if readErr != nil {
		sr.logger.Sugar().Errorf("Failed to read request body: %+v", readErr)
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if c, ok := pr.(io.Closer); ok {
		_ = c.Close()
	}
	_ = r.Body.Close()

	var td ptrace.Traces
	envlp, err := sr.ParseEnvelopEvent(string(slurp))
	if err != nil {
		sr.logger.Sugar().Errorf("Error parsing envelop: %+v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		if _, wErr := w.Write([]byte("{}")); wErr != nil {
			sr.logger.Sugar().Errorf("Failed to write response: %+v", wErr)
		}
		return
	}

	td, err = sr.toTraceSpans(envlp, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sr.logger.Sugar().Debugf("For %v got trace with %v SpanCount(): %+v", envlp.Type, td.SpanCount(), td)

	consumerErr := sr.nextConsumer.ConsumeTraces(ctx, td)
	sr.obsrecvr.EndTracesOp(ctx, "sentryReceiverTagValue", td.SpanCount(), consumerErr)

	if consumerErr == nil {
		var resp []byte

		if envlp.EnvelopType == models.ENVELOP_TYPE_SESSION {
			resp = []byte("{}")
		} else {
			resp = []byte(fmt.Sprintf("{\"id\": \"%v\"}", envlp.EventID))
		}
		if _, wErr := w.Write(resp); wErr != nil {
			sr.logger.Sugar().Errorf("Failed to write response: %+v", wErr)
		}
		return
	}

	sr.logger.Sugar().Errorf("Consumer error: %+v", consumerErr)

	if consumererror.IsPermanent(consumerErr) {
		w.WriteHeader(http.StatusBadRequest)
		if _, wErr := w.Write(errBadRequestRespBody); wErr != nil {
			sr.logger.Sugar().Errorf("Failed to write error response: %+v", wErr)
		}
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		if _, wErr := w.Write(errNextConsumerRespBody); wErr != nil {
			sr.logger.Sugar().Errorf("Failed to write error response: %+v", wErr)
		}
	}
}

func processBodyIfNecessary(req *http.Request) io.Reader {
	switch req.Header.Get("Content-Encoding") {
	default:
		return req.Body
	case "gzip":
		return gunzippedBodyIfPossible(req.Body)
	case "deflate", "zlib":
		return zlibUncompressedbody(req.Body)
	}
}

func gunzippedBodyIfPossible(r io.Reader) io.Reader {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return r
	}
	return gzr
}

func zlibUncompressedbody(r io.Reader) io.Reader {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return r
	}
	return zr
}

func (sr *sentrytraceReceiver) toTraceSpans(envlp *models.EnvelopEventParseResult, r *http.Request) (ptrace.Traces, error) {
	traces := ptrace.NewTraces()
	resourceSpan := traces.ResourceSpans().AppendEmpty()
	resource := resourceSpan.Resource()
	sr.fillResource(&resource, envlp, r)
	scopeSpans := resourceSpan.ScopeSpans().AppendEmpty()

	switch envlp.EnvelopType {
	case models.ENVELOP_TYPE_SESSION:
		sr.appendScopeSpansForSessionEvent(&scopeSpans, envlp, r)
	case models.ENVELOP_TYPE_TRANSACTION, models.ENVELOP_TYPE_EVENT:
		sr.appendScopeSpans(&scopeSpans, envlp, r)
	default:
		sr.logger.Sugar().Warnf("Unknown EnvelopType: %v", envlp.EnvelopType)
	}

	return traces, nil
}

func (sr *sentrytraceReceiver) fillResource(resource *pcommon.Resource, envlp *models.EnvelopEventParseResult, r *http.Request) {
	attrs := resource.Attributes()
	attrs.PutStr(conventions.AttributeTelemetrySDKName, envlp.Name)
	attrs.PutStr(conventions.AttributeServiceName, sr.GetServiceName(r))
	attrs.PutStr("trace.source.type", "sentry")
}

func (sr *sentrytraceReceiver) appendScopeSpans(scopeSpans *ptrace.ScopeSpans, envlp *models.EnvelopEventParseResult, r *http.Request) {
	for _, event := range envlp.Events {
		rootSpan := scopeSpans.Spans().AppendEmpty()
		var startTime, endTime time.Time

		rootSpan.SetTraceID(sr.GenerateTraceID(event.Contexts.Trace.TraceID))
		eventTransaction := event.Transaction
		eventTransactionPath := sr.removeIdFromURL(eventTransaction)

		switch envlp.EnvelopType {
		case models.ENVELOP_TYPE_TRANSACTION:
			rootSpan.SetName(eventTransactionPath + " " + event.Contexts.Trace.Op)
			rootSpan.SetSpanID(sr.GenerateSpanId(event.Contexts.Trace.SpanID))
			startTime = GetUnixTimeFromFloat64(event.StartTimestamp)
			endTime = GetUnixTimeFromFloat64(event.Timestamp)

		case models.ENVELOP_TYPE_EVENT:
			endTime = GetUnixTimeFromFloat64(event.Timestamp)
			startTime = endTime
			rootSpan.SetSpanID(sr.GenerateSpanId(event.EventId[0:16]))
			rootSpan.SetParentSpanID(sr.GenerateSpanId(event.Contexts.Trace.SpanID))
			rootSpan.SetName("Event")

			level := event.Level
			if level != "" {
				rootSpan.Attributes().PutStr("level", level)
			}
			if level == "error" || level == "fatal" {
				rootSpan.Status().SetCode(ptrace.StatusCodeError)
			}

			sdkName := ""
			if event.Sdk.Name != "" {
				sdkName = event.Sdk.Name
			}
			sdkVersion := ""
			if event.Sdk.Version != "" {
				sdkVersion = event.Sdk.Version
			}
			if sdkName != "" || sdkVersion != "" {
				rootSpan.Attributes().PutStr("sdk", sdkName+"@"+sdkVersion)
			}

			if msg := event.Message; msg != "" {
				rootSpan.Attributes().PutStr("message", string(msg))
			}
			if ns := event.Namespace; ns != "" {
				rootSpan.Attributes().PutStr("namespace", ns)
			}
			if exc := event.Exception.Values; len(exc) > 0 {
				rootSpan.Attributes().PutStr("exception.values", fmt.Sprintf("%+v", exc))
			}

			if ctxJSON, err := json.Marshal(event.Contexts); err == nil && string(ctxJSON) != "" {
				rootSpan.Attributes().PutStr("contexts", string(ctxJSON))
			}

			if ctxErr := event.Contexts.Error; ctxErr.Message != "" || ctxErr.Name != "" || ctxErr.Stack != "" {
				rootSpan.Attributes().PutStr("context.error", fmt.Sprintf("%+v", ctxErr))
			}

			if ts := event.Timestamp; ts != 0 {
				rootSpan.Attributes().PutDouble("timestamp", ts)
			}
			if eid := event.EventId; eid != "" {
				rootSpan.Attributes().PutStr("event_id", eid)
			}
			if rel := event.Release; rel != "" {
				rootSpan.Attributes().PutStr("version", rel)
			}
			if platform := event.Platform; platform != "" {
				rootSpan.Attributes().PutStr("platform", platform)
			}
			if uid := event.User.Id; uid != "" {
				rootSpan.Attributes().PutStr("user_id", uid)
			}
			if trx, ok := event.Tags["transaction"].(string); ok && trx != "" {
				rootSpan.Attributes().PutStr("tags.transaction", trx)
			}
			if logger := event.Logger; logger != "" {
				rootSpan.Attributes().PutStr("category", logger)
			} else {
				rootSpan.Attributes().PutStr("category", "frontend-event")
			}
			if ua := event.Request.Headers["User-Agent"]; ua != "" {
				rootSpan.Attributes().PutStr("browser", ua)
			}
		default:
			sr.logger.Sugar().Warnf("Unknown EnvelopType: %v", envlp.EnvelopType)
		}

		rootSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(startTime))
		rootSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(endTime))
		rootSpan.Attributes().PutInt("sentry.envelop.type.int", int64(envlp.EnvelopType))

		if name := sr.GetServiceName(r); name != "" {
			rootSpan.Attributes().PutStr("name", name)
		}
		if serviceName := r.Header.Get("x-service-name"); serviceName != "" {
			rootSpan.Attributes().PutStr("service.name", serviceName)
		}
		if spanId := event.Contexts.Trace.SpanID; spanId != "" {
			rootSpan.Attributes().PutStr("contexts.trace.span_id", spanId)
		}
		if traceId := event.Contexts.Trace.TraceID; traceId != "" {
			rootSpan.Attributes().PutStr("contexts.trace.trace_id", traceId)
		}
		if eventTransaction != "" {
			rootSpan.Attributes().PutStr("transaction", eventTransaction)
			rootSpan.Attributes().PutStr("transaction_path", eventTransactionPath)
		}
		if op := event.Contexts.Trace.Op; op != "" {
			rootSpan.Attributes().PutStr("operation", op)
		}
		if url := event.Request.URL; url != "" {
			rootSpan.Attributes().PutStr("url", url)
		}
		if dist := event.Dist; dist != "" {
			rootSpan.Attributes().PutStr("dist", dist)
		}
		if env := event.Environment; env != "" {
			rootSpan.Attributes().PutStr("environment", env)
		}

		measurements := rootSpan.Attributes().PutEmptyMap("measurements")
		for k, m := range event.Measurements {
			mMap := measurements.PutEmptyMap(k)
			mMap.PutDouble("value", m.Value)
			mMap.PutStr("unit", m.Unit)
		}
		rootSpan.SetKind(ptrace.SpanKindClient)

		for k, v := range event.Tags {
			rootSpan.Attributes().PutStr("tags."+k, fmt.Sprintf("%v", v))
		}

		if reqURL := event.Request.URL; reqURL != "" {
			if urlParsed, err := url.Parse(reqURL); err == nil {
				for _, qParam := range sr.config.HttpQueryParamValuesToAttrs {
					qValue := urlParsed.Query().Get(qParam)
					rootSpan.Attributes().PutStr("http.qparam."+qParam, qValue)
					sr.logger.Sugar().Debugf("Value QParam %v with value %v is found", qParam, qValue)
				}
				for _, qParam := range sr.config.HttpQueryParamExistenceToAttrs {
					val := urlParsed.Query().Get(qParam)
					if val != "" {
						val = "true"
					} else {
						val = "false"
					}
					rootSpan.Attributes().PutStr("http.qparam."+qParam, val)
					sr.logger.Sugar().Debugf("Existence QParam %v with value %v is found", qParam, val)
				}
				rootSpan.Attributes().PutStr("url_path", sr.removeIdFromURL(urlParsed.Path))
			} else {
				sr.logger.Sugar().Errorf("Error parsing url request %v : %+v", reqURL, err)
			}
		}

		for _, contextParam := range sr.config.ContextSpanAttributesList {
			val := event.Contexts.AsMap[contextParam]
			if val == nil {
				continue
			}
			switch valTyped := val.(type) {
			case string:
				rootSpan.Attributes().PutStr("contexts."+contextParam, valTyped)
			case map[string]interface{}:
				for k, v := range valTyped {
					rootSpan.Attributes().PutStr(fmt.Sprintf("contexts.%v.%v", contextParam, k), fmt.Sprintf("%v", v))
				}
			}
		}

		breadcrumbs := rootSpan.Attributes().PutEmptySlice("breadcrumbs")
		for _, envBr := range event.Breadcrumbs {
			dataJson, err := json.Marshal(envBr.Data)
			if err != nil {
				dataJson = []byte(fmt.Sprintf("%v", envBr.Data))
			}
			dataStr := string(dataJson)
			bc := breadcrumbs.AppendEmpty()
			bcMap := bc.SetEmptyMap()
			bcMap.PutStr("level", envBr.Level)
			bcMap.PutDouble("timestamp", envBr.Timestamp)
			bcMap.PutStr("category", envBr.Category)
			bcMap.PutStr("message", string(envBr.Message))
			bcMap.PutStr("data", dataStr)
		}

		rootSpan.Attributes().PutStr(conventions.AttributeEnduserID, event.User.Id)

		for _, sentrySpan := range event.Spans {
			span := scopeSpans.Spans().AppendEmpty()
			startTime := GetUnixTimeFromFloat64(sentrySpan.StartTimestamp)
			endTime := GetUnixTimeFromFloat64(sentrySpan.Timestamp)
			span.SetTraceID(sr.GenerateTraceID(sentrySpan.TraceId))
			span.SetSpanID(sr.GenerateSpanId(sentrySpan.SpanId))
			span.SetParentSpanID(sr.GenerateSpanId(sentrySpan.ParentSpanId))
			span.SetStartTimestamp(pcommon.NewTimestampFromTime(startTime))
			span.SetEndTimestamp(pcommon.NewTimestampFromTime(endTime))
			span.SetName(sentrySpan.Op)

			var httpStatusCode string
			if sentrySpan.Data != nil {
				httpStatusCode = fmt.Sprintf("%v", sentrySpan.Data["http.response.status_code"])
			}
			if httpStatusCode != "" {
				if code, err := strconv.ParseInt(httpStatusCode, 10, 64); err == nil {
					if code < 400 {
						span.Status().SetCode(ptrace.StatusCodeOk)
					} else {
						span.Status().SetCode(ptrace.StatusCodeError)
					}
				} else {
					span.Status().SetCode(ptrace.StatusCodeUnset)
				}
			} else {
				span.Status().SetCode(ptrace.StatusCodeUnset)
			}

			if url := sentrySpan.Data["url"]; url != nil {
				span.Attributes().PutStr("url_path", fmt.Sprintf("%v", url))
			}

			for k, v := range sentrySpan.Data {
				if timestampSpanDataAttributes[k] {
					val, ok := v.(float64)
					if ok {
						span.Attributes().PutDouble(k, val)
						continue
					}
				}
				switch valTyped := v.(type) {
				case float64:
					_, frac := math.Modf(valTyped)
					if frac == 0 {
						span.Attributes().PutInt(k, int64(valTyped))
					} else {
						span.Attributes().PutDouble(k, valTyped)
					}
				case string:
					span.Attributes().PutStr(k, valTyped)
				default:
					span.Attributes().PutStr(k, fmt.Sprintf("%v", v))
				}
			}
			for k, v := range sentrySpan.Tags {
				span.Attributes().PutStr("tags."+k, fmt.Sprintf("%v", v))
			}
			if sentrySpan.Origin != "" {
				span.Attributes().PutStr("origin", sentrySpan.Origin)
			}
			if sentrySpan.Description != "" {
				span.Attributes().PutStr("description", sentrySpan.Description)
			}
			span.SetKind(ptrace.SpanKindClient)
		}
	}
}

func (sr *sentrytraceReceiver) appendScopeSpansForSessionEvent(scopeSpans *ptrace.ScopeSpans, envlp *models.EnvelopEventParseResult, r *http.Request) {
	for _, event := range envlp.SessionEvents {
		sr.logger.Sugar().Debugf("Recieved session event event.Sid = %v", event.Sid)
		rootSpan := scopeSpans.Spans().AppendEmpty()
		rootSpan.SetTraceID(sr.GenerateTraceID(removeHyphens(event.Sid)))
		rootSpan.SetName("Session " + event.Sid)
		rootSpan.SetSpanID(sr.GenerateSpanId(removeHyphens(event.Sid)[0:16]))
		timestamp, err := time.Parse(time.RFC3339, event.Timestamp)
		if err != nil {
			sr.logger.Sugar().Errorf("Error parsing timestamp %v for session event : %+v", event.Timestamp, err)
		} else {
			rootSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(timestamp))
		}
		rootSpan.Attributes().PutInt("sentry.envelop.type.int", models.ENVELOP_TYPE_SESSION)
		name := sr.GetServiceName(r)
		if name != "" {
			rootSpan.Attributes().PutStr("name", name)
		}
		serviceName := r.Header.Get("x-service-name")
		if serviceName != "" {
			rootSpan.Attributes().PutStr("service.name", serviceName)
		}
		rootSpan.Attributes().PutStr("session.status", event.Status)
		rootSpan.Attributes().PutStr("sentry.envelop.type", "session")
		rootSpan.SetKind(ptrace.SpanKindClient)
	}
}

func (sr *sentrytraceReceiver) GenerateTraceID(str string) pcommon.TraceID {
	data, err := hex.DecodeString(str)
	if err != nil {
		sr.logger.Sugar().Errorf("SentryReceiver : GenerateTraceID : Can not decode str %v to bytes : %+v", str, err)
		return pcommon.TraceID([16]byte{})
	}

	result := (*[16]byte)(data)

	return pcommon.TraceID(*result)
}

func (sr *sentrytraceReceiver) GenerateSpanId(str string) pcommon.SpanID {
	data, err := hex.DecodeString(str)
	if err != nil {
		sr.logger.Sugar().Errorf("SentryReceiver : GenerateSpanId : Can not decode str %v to bytes : %+v", str, err)
		return pcommon.SpanID([8]byte{})
	}

	result := (*[8]byte)(data)

	return pcommon.SpanID(*result)
}

func GetUnixTimeFromFloat64(timeFloat64 float64) time.Time {
	sec, dec := math.Modf(timeFloat64)
	return time.Unix(int64(sec), int64(dec*(1e9)))
}

func (sr *sentrytraceReceiver) removeIdFromURL(urlStr string) string {
	if strings.HasPrefix(urlStr, "http://") || strings.HasPrefix(urlStr, "https://") {
		u, err := url.Parse(urlStr)
		if err != nil {
			return "NON_PARSABLE_URL"
		}
		return u.Scheme + "://" + u.Host + utils.RemoveIDsFromURI(u.Path, "_UUID_", "_NUMBER_", "_ID_", 4, "_ID_", 8)
	}
	return utils.RemoveIDsFromURI(urlStr, "_UUID_", "_NUMBER_", "_ID_", 4, "_ID_", 8)
}

func removeHyphens(input string) string {
	return strings.ReplaceAll(input, "-", "")
}

func (sr *sentrytraceReceiver) GetServiceName(r *http.Request) string {
	name := r.Header.Get("x-service-id")
	if name != "" {
		return name
	}
	trimmedPath := strings.Trim(r.URL.Path, "/ ")
	pathElements := strings.Split(trimmedPath, "/")
	if len(pathElements) > 0 {
		return pathElements[0]
	}
	return ""
}
