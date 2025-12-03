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
	"net"
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

func (sr *sentrytraceReceiver) Start(_ context.Context, host component.Host) error {
	sr.host = host
	var ctx = context.Background()
	ctx, sr.cancel = context.WithCancel(ctx)

	sr.logger.Info("SentryReceiver started")
	if host == nil {
		return errors.New("nil host")
	}

	var err error
	sr.server, err = sr.config.ServerConfig.ToServer(ctx, host.GetExtensions(), sr.settings.TelemetrySettings, sr)
	if err != nil {
		return err
	}

	var listener net.Listener
	listener, err = sr.config.ServerConfig.ToListener(ctx)
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

func (sr *sentrytraceReceiver) Shutdown(_ context.Context) error {
	sr.logger.Info("SentryReceiver is shutdown")

	return nil
}

func (sr *sentrytraceReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ctx = sr.obsrecvr.StartTracesOp(ctx)

	pr := processBodyIfNecessary(r)
	slurp, _ := io.ReadAll(pr)
	if c, ok := pr.(io.Closer); ok {
		_ = c.Close()
	}
	_ = r.Body.Close()

	var td ptrace.Traces
	var err error
	envlp, err := sr.ParseEnvelopEvent(string(slurp))
	if err != nil {
		sr.logger.Sugar().Errorf("SentryReceiver : Error parsing envelop : %+v", err)
		w.WriteHeader(http.StatusNotAcceptable)
		if _, writeErr := w.Write([]byte("{}")); writeErr != nil {
			sr.logger.Sugar().Errorf("SentryReceiver : Error writing response: %+v", writeErr)
		}
		return
	}
	td, err = sr.toTraceSpans(envlp, r)

	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	sr.logger.Sugar().Debugf("SentryReceiver : For %v got trace with %v SpanCount() : %+v", envlp.Type, td.SpanCount(), td)

	consumerErr := sr.nextConsumer.ConsumeTraces(ctx, td)

	sr.obsrecvr.EndTracesOp(ctx, "sentryReceiverTagValue", td.SpanCount(), consumerErr)
	if consumerErr == nil {
		if envlp.EnvelopType == models.ENVELOP_TYPE_SESSION {
			if _, writeErr := w.Write([]byte("{}")); writeErr != nil {
				sr.logger.Sugar().Errorf("SentryReceiver : Error writing response: %+v", writeErr)
			}
		} else {
			if err := json.NewEncoder(w).Encode(map[string]string{"id": envlp.EventID}); err != nil {
				sr.logger.Sugar().Errorf("SentryReceiver : Error writing response: %+v", err)
			}
		}
		return
	}
	sr.logger.Sugar().Errorf("SentryReceiver : Consumer error : %+v", consumerErr)

	if consumererror.IsPermanent(consumerErr) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(errBadRequestRespBody)
	} else {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(errNextConsumerRespBody)
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

func (sr *sentrytraceReceiver) toTraceSpans(envlp *models.EnvelopEventParseResult, r *http.Request) (reqs ptrace.Traces, err error) {
	traces := ptrace.NewTraces()
	resourceSpan := traces.ResourceSpans().AppendEmpty()
	resource := resourceSpan.Resource()
	sr.fillResource(&resource, envlp, r)
	scopeSpans := resourceSpan.ScopeSpans().AppendEmpty()
	if envlp.EnvelopType == models.ENVELOP_TYPE_SESSION {
		sr.appendScopeSpansForSessionEvent(&scopeSpans, envlp, r)
	} else {
		sr.appendScopeSpans(&scopeSpans, envlp, r)
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
			rootSpan.SetName("Event") //+ event.EventId)

			level := sr.evaluateLevel(event)
			if level != "" {
				rootSpan.Attributes().PutStr("level", level)
			}
			if level == "error" || level == "fatal" {
				rootSpan.Status().SetCode(ptrace.StatusCodeError)
			}

			sdk := event.Sdk.Name + "@" + event.Sdk.Version
			if sdk != "@" {
				rootSpan.Attributes().PutStr("sdk", sdk)
			}
			message := event.Message
			if message != "" {
				rootSpan.Attributes().PutStr("message", string(message))
			}
			exceptionValues := event.Exception.Values
			if len(exceptionValues) > 0 {
				rootSpan.Attributes().PutStr("exception.values", fmt.Sprintf("%+v", exceptionValues))
			}
			contextError := event.Contexts.Error
			if contextError.Message != "" || contextError.Name != "" || contextError.Stack != "" {
				rootSpan.Attributes().PutStr("context.error", fmt.Sprintf("%+v", contextError))
			}
			timestamp := event.Timestamp
			if timestamp != 0 {
				rootSpan.Attributes().PutDouble("timestamp", timestamp)
			}
			eventId := event.EventId
			if eventId != "" {
				rootSpan.Attributes().PutStr("event_id", eventId)
			}
			release := event.Release
			if release != "" {
				rootSpan.Attributes().PutStr("version", release)
			}
			platform := event.Platform
			if platform != "" {
				rootSpan.Attributes().PutStr("platform", platform)
			}
			userId := event.User.Id
			if userId != "" {
				rootSpan.Attributes().PutStr("user_id", userId)
			}
			transaction, ok := event.Tags["transaction"].(string)
			if ok && transaction != "" {
				rootSpan.Attributes().PutStr("tags.transaction", transaction)
			}
			logger := event.Logger
			if logger != "" {
				rootSpan.Attributes().PutStr("category", logger)
			} else {
				rootSpan.Attributes().PutStr("category", "frontend-event")
			}
			userAgent := event.Request.Headers["User-Agent"]
			if userAgent != "" {
				rootSpan.Attributes().PutStr("browser", userAgent)
			}
		}
		rootSpan.SetStartTimestamp(pcommon.NewTimestampFromTime(startTime))
		rootSpan.SetEndTimestamp(pcommon.NewTimestampFromTime(endTime))

		rootSpan.Attributes().PutInt("sentry.envelop.type.int", int64(envlp.EnvelopType))
		name := sr.GetServiceName(r)
		if name != "" {
			rootSpan.Attributes().PutStr("name", name)
		}
		serviceName := r.Header.Get("x-service-name")
		if serviceName != "" {
			rootSpan.Attributes().PutStr("service.name", serviceName)
		}
		spanId := event.Contexts.Trace.SpanID
		if spanId != "" {
			rootSpan.Attributes().PutStr("contexts.trace.span_id", spanId)
		}
		traceId := event.Contexts.Trace.TraceID
		if traceId != "" {
			rootSpan.Attributes().PutStr("contexts.trace.trace_id", traceId)
		}
		if eventTransaction != "" {
			rootSpan.Attributes().PutStr("transaction", eventTransaction)
			rootSpan.Attributes().PutStr("transaction_path", eventTransactionPath)
		}

		if event.Contexts.Trace.Op != "" {
			rootSpan.Attributes().PutStr("operation", event.Contexts.Trace.Op)
		}

		if event.Request.URL != "" {
			rootSpan.Attributes().PutStr("url", event.Request.URL)
		}
		if event.Dist != "" {
			rootSpan.Attributes().PutStr("dist", event.Dist)
		}
		if event.Environment != "" {
			rootSpan.Attributes().PutStr("environment", event.Environment)
		}

		measurements := rootSpan.Attributes().PutEmptyMap("measurements")
		for k, m := range event.Measurements {
			measurementMapInstance := measurements.PutEmptyMap(k)
			measurementMapInstance.PutDouble("value", m.Value)
			measurementMapInstance.PutStr("unit", m.Unit)
		}
		rootSpan.SetKind(ptrace.SpanKindClient)

		for k, v := range event.Tags {
			rootSpan.Attributes().PutStr("tags."+k, fmt.Sprintf("%v", v))
		}

		requestUrlStr := event.Request.URL
		if requestUrlStr != "" {
			urlParsed, err := url.Parse(requestUrlStr)
			if err != nil {
				sr.logger.Sugar().Errorf("Error parsing url request %v : %+v", requestUrlStr, err)
			} else {
				for _, qParam := range sr.config.HttpQueryParamValuesToAttrs {
					qValue := urlParsed.Query().Get(qParam)
					rootSpan.Attributes().PutStr("http.qparam."+qParam, qValue)
					sr.logger.Sugar().Debugf("Value QParam %v with value %v is found", qParam, qValue)
				}
				for _, qParam := range sr.config.HttpQueryParamExistenceToAttrs {
					qValue := urlParsed.Query().Get(qParam)
					if qValue != "" {
						qValue = "true"
					} else {
						qValue = "false"
					}
					sr.logger.Sugar().Debugf("Existence QParam %v with value %v is found", qParam, qValue)
					rootSpan.Attributes().PutStr("http.qparam."+qParam, qValue)
				}
				rootSpan.Attributes().PutStr("url_path", sr.removeIdFromURL(urlParsed.Path))
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
			if envBr.Type == "http" {
				breadcrumb := breadcrumbs.AppendEmpty()
				breadcrumbMap := breadcrumb.SetEmptyMap()
				breadcrumbMap.PutStr("level", envBr.Level)
				breadcrumbMap.PutDouble("timestamp", envBr.Timestamp)
				breadcrumbMap.PutStr("category", envBr.Category)
				breadcrumbMap.PutStr("message", fmt.Sprintf("%v %v", envBr.Data["method"], envBr.Data["url"]))
				breadcrumbMap.PutStr("status", fmt.Sprintf("%v", envBr.Data["status_code"]))
			} else if envBr.Category == "navigation" {
				breadcrumb := breadcrumbs.AppendEmpty()
				breadcrumbMap := breadcrumb.SetEmptyMap()
				breadcrumbMap.PutDouble("timestamp", envBr.Timestamp)
				breadcrumbMap.PutStr("category", "navigation")
				breadcrumbMap.PutStr("message", fmt.Sprintf("Browser navigation from: %v to: %v", envBr.Data["from"], envBr.Data["to"]))
			} else if envBr.Category == "console" {
				breadcrumb := breadcrumbs.AppendEmpty()
				breadcrumbMap := breadcrumb.SetEmptyMap()
				breadcrumbMap.PutStr("level", envBr.Level)
				breadcrumbMap.PutDouble("timestamp", envBr.Timestamp)
				breadcrumbMap.PutStr("category", "console")
				breadcrumbMap.PutStr("message", string(envBr.Message))
			} else {
				breadcrumb := breadcrumbs.AppendEmpty()
				breadcrumbMap := breadcrumb.SetEmptyMap()
				breadcrumbMap.PutStr("category", "console")
				breadcrumbMap.PutStr("message", string(envBr.Message))
			}
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
				httpStatusCodeInt, err := strconv.ParseInt(httpStatusCode, 10, 64)
				if err != nil {
					span.Status().SetCode(ptrace.StatusCodeUnset)
				} else {
					if httpStatusCodeInt < 400 {
						span.Status().SetCode(ptrace.StatusCodeOk)
					} else {
						span.Status().SetCode(ptrace.StatusCodeError)
					}
				}
			} else {
				span.Status().SetCode(ptrace.StatusCodeUnset)
			}

			url := sentrySpan.Data["url"]
			if url != nil {
				switch urlTyped := url.(type) {
				case string:
					span.Attributes().PutStr("url_path", sr.removeIdFromURL(urlTyped))
				default:
					span.Attributes().PutStr("url_path", sr.removeIdFromURL(fmt.Sprintf("%v", urlTyped)))
				}
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
					const epsilon = 1e-9
					_, frac := math.Modf(valTyped)
					frac = math.Abs(frac)
					if frac < epsilon || frac > 1.0-epsilon {
						span.Attributes().PutInt(k, int64(math.Round(valTyped)))
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

var levelRating = map[string]int{
	"fatal":   6,
	"error":   5,
	"warning": 4,
	"log":     3,
	"info":    2,
	"debug":   1,
}

var ratingLevel = map[int]string{
	6: "fatal",
	5: "error",
	4: "warning",
	3: "log",
	2: "info",
	1: "debug",
}

func (sr *sentrytraceReceiver) evaluateLevel(event models.Event) string {
	if sr.config.LevelEvaluationStrategy == "" {
		return event.Level
	}
	maxLevel := levelRating[event.Level]
	for _, envBr := range event.Breadcrumbs {
		brLevel := levelRating[envBr.Level]
		if brLevel > maxLevel {
			maxLevel = brLevel
		}
	}
	return ratingLevel[maxLevel]
}

func (sr *sentrytraceReceiver) appendScopeSpansForSessionEvent(scopeSpans *ptrace.ScopeSpans, envlp *models.EnvelopEventParseResult, r *http.Request) {
	for _, event := range envlp.SessionEvents {
		sr.logger.Sugar().Debugf("Received session event event.Sid = %v", event.Sid)
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
