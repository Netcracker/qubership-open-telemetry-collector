// Copyright 2025 Qubership
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package models

import (
	"encoding/json"
	"fmt"
)

const (
	ENVELOP_TYPE_UNKNOWN     = 0
	ENVELOP_TYPE_TRANSACTION = 1
	ENVELOP_TYPE_EVENT       = 2
	ENVELOP_TYPE_SESSION     = 3
)

type SdkInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type EnvelopTypeHeader struct {
	Type   string `json:"type"`
	Length int    `json:"length,omitempty"`
}
type EnvelopEventHeader struct {
	SdkInfo `json:"sdk,omitempty"`
	EventID string `json:"event_id,omitempty"`
}

type EventMeasurement struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}
type EventMeasurements map[string]EventMeasurement

type StrongString string

type Breadcrumb struct {
	Type      string                 `json:"type,omitempty"`
	Level     string                 `json:"level,omitempty"`
	Message   StrongString           `json:"message,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Category  string                 `json:"category,omitempty"`
	Timestamp float64                `json:"timestamp,omitempty"`
}

func (d *StrongString) UnmarshalJSON(data []byte) error {
	var maxAllowedLength = 50 * 1024 * 1024 // Limit Message to 50 MB
	if len(data) > maxAllowedLength {
		return fmt.Errorf("StrongString: data length %d is more than allowed length %d", len(data), maxAllowedLength)
	}
	// formattedData := StrongString(string(data))
	*d = StrongString(string(data))
	return nil
}

type EventRequest struct {
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type Event struct {
	Message        StrongString                `json:"message,omitempty"`
	Level          string                      `json:"level,omitempty"`
	EventId        string                      `json:"event_id,omitempty"`
	Platform       string                      `json:"platform,omitempty"`
	Dist           string                      `json:"dist,omitempty"`
	Timestamp      float64                     `json:"timestamp,omitempty"`
	StartTimestamp float64                     `json:"start_timestamp,omitempty"`
	Environment    string                      `json:"environment,omitempty"`
	Release        string                      `json:"release,omitempty"`
	Transaction    string                      `json:"transaction,omitempty"`
	Measurements   map[string]EventMeasurement `json:"measurements,omitempty"`
	Breadcrumbs    []Breadcrumb                `json:"breadcrumbs,omitempty"`
	User           EventUser                   `json:"user,omitempty"`
	Contexts       EventContexts               `json:"contexts,omitempty"`
	Tags           map[string]interface{}      `json:"tags,omitempty"`
	Spans          []EventSpan                 `json:"spans,omitempty"`
	Request        EventRequest                `json:"request,omitempty"`
	Sdk            SdkInfo                     `json:"sdk,omitempty"`
	Exception      EventException              `json:"exception,omitempty"`
	Logger         string                      `json:"logger,omitempty"`
}

type SessionEvent struct {
	Status    string `json:"status,omitempty"`
	Sid       string `json:"sid,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type EventException struct {
	Values []struct {
		Type       string       `json:"type,omitempty"`
		Value      StrongString `json:"value,omitempty"`
		Stacktrace struct {
			Frames []struct {
				Filename string `json:"filename,omitempty"`
				Function string `json:"function,omitempty"`
				InApp    bool   `json:"in_app,omitempty"`
				Lineno   int    `json:"lineno,omitempty"`
				Colno    int    `json:"colno,omitempty"`
			} `json:"frames,omitempty"`
		} `json:"stacktrace,omitempty"`
		Mechanism struct {
			Type      string `json:"type,omitempty"`
			Handled   bool   `json:"handled,omitempty"`
			Synthetic bool   `json:"synthetic,omitempty"`
		} `json:"mechanism,omitempty"`
	} `json:"values,omitempty"`
}

type EventUser struct {
	Id string `json:"id,omitempty"`
}

type EventContexts struct {
	Trace struct {
		Op      string `json:"op,omitempty"`
		SpanID  string `json:"span_id,omitempty"`
		TraceID string `json:"trace_id,omitempty"`
	} `json:"trace,omitempty"`
	Error ContextError           `json:"Error,omitempty"`
	AsMap map[string]interface{} `json:"-"`
}

type _EventContexts EventContexts

func (f *EventContexts) UnmarshalJSON(bs []byte) (err error) {
	eventContexts := _EventContexts{}

	if err = json.Unmarshal(bs, &eventContexts); err == nil {
		*f = EventContexts(eventContexts)
	} else {
		return err
	}

	asMap := make(map[string]interface{})
	if err = json.Unmarshal(bs, &asMap); err == nil {
		f.AsMap = asMap
	}

	return err
}

type ContextError struct {
	Config       ContextErrorConfig   `json:"config,omitempty"`
	Request      ContextErrorRequest  `json:"request,omitempty"`
	Response     ContextErrorResponse `json:"response,omitempty"`
	IsAxiosError bool                 `json:"isAxiosError,omitempty"`
	Message      string               `json:"message,omitempty"`
	Name         string               `json:"name,omitempty"`
	Stack        string               `json:"stack,omitempty"`
	Status       int                  `json:"status,omitempty"`
}

type ContextErrorConfig struct {
	Headers map[string]string `json:"headers,omitempty"`
	BaseUrl string            `json:"baseURL,omitempty"`
	Method  string            `json:"method,omitempty"`
	Url     string            `json:"url,omitempty"`
}

type ContextErrorRequest struct {
	SentryXhrV3 struct {
		Method         string       `json:"method,omitempty"`
		Url            string       `json:"url,omitempty"`
		RequestHeaders StrongString `json:"request_headers,omitempty"`
		StatusCode     int          `json:"status_code,omitempty"`
	} `json:"__sentry_xhr_v3__,omitempty"`
	SetRequestHeader string `json:"setRequestHeader,omitempty"`
	SentryXhrSpanId  string `json:"__sentry_xhr_span_id__,omitempty"`
}

type ContextErrorResponse struct {
	Data       StrongString           `json:"data,omitempty"`
	Status     int                    `json:"status,omitempty"`
	StatusText string                 `json:"statusText,omitempty"`
	Headers    map[string]string      `json:"headers,omitempty"`
	Config     map[string]interface{} `json:"config,omitempty"`
	Request    map[string]interface{} `json:"request,omitempty"`
}

type EventSpan struct {
	Description    string                 `json:"description"`
	SpanId         string                 `json:"span_id"`
	ParentSpanId   string                 `json:"parent_span_id"`
	Origin         string                 `json:"origin"`
	Op             string                 `json:"op,omitempty"`
	Tags           map[string]interface{} `json:"tags,omitempty"`
	Status         string                 `json:"status,omitempty"`
	Data           map[string]interface{} `json:"data,omitempty"`
	TraceId        string                 `json:"trace_id"`
	Timestamp      float64                `json:"timestamp,omitempty"`
	StartTimestamp float64                `json:"start_timestamp,omitempty"`
}

type EnvelopEventParseResult struct {
	EnvelopTypeHeader  `json:"type_header,omitempty"`
	EnvelopEventHeader `json:"header,omitempty"`
	Events             []Event        `json:"events,omitempty"`
	SessionEvents      []SessionEvent `json:"session-events,omitempty"`
	EnvelopType        int            `json:"envelop-type,omitempty"`
}
