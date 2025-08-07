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

package sentryreceiver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Netcracker/qubership-open-telemetry-collector/receiver/sentryreceiver/models"
)

func (sr *sentrytraceReceiver) ParseEnvelopEvent(body string) (*models.EnvelopEventParseResult, error) {
	logger := sr.logger
	logger.Sugar().Debugf("SentryReceiver : Start parsing envelop :\n---START---\n%+v\n---END---\n", body)
	lines := strings.Split(body, "\n")

	var header models.EnvelopEventHeader
	var type_header models.EnvelopTypeHeader
	events := make([]models.Event, 0)
	sessionEvents := make([]models.SessionEvent, 0)
	linesCount := len(lines)
	if linesCount < 3 {
		return nil, fmt.Errorf("SentryReceiver : Unexpected number of lines in the envelope : %v. Must be 3 or greater", linesCount)
	}

	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		logger.Sugar().Errorf("SentryReceiver : Unmarshal header error: %+v", err.Error())
		return nil, err
	}

	var envelopType = models.ENVELOP_TYPE_UNKNOWN
	for i := 1; i+1 < linesCount; i += 2 {
		header := lines[i]
		if len(header) < 2 {
			continue
		}
		payload := lines[i+1]
		if len(payload) < 2 {
			continue
		}
		if err := json.Unmarshal([]byte(header), &type_header); err != nil {
			logger.Sugar().Errorf("SentryReceiver : Unmarshal type_header error: %+v", err.Error())
			return nil, err
		}
		switch type_header.Type {
		case "transaction":
			envelopType = models.ENVELOP_TYPE_TRANSACTION
		case "event":
			envelopType = models.ENVELOP_TYPE_EVENT
		case "session":
			envelopType = models.ENVELOP_TYPE_SESSION
		default:
			logger.Sugar().Infof("SentryReceiver : Received %v item header. Skipping this item", type_header.Type)
			continue
		}

		if envelopType == models.ENVELOP_TYPE_SESSION {
			var sessionEvent models.SessionEvent
			if err := json.Unmarshal([]byte(payload), &sessionEvent); err != nil {
				logger.Sugar().Errorf("SentryReceiver : Unmarshal session event error: %+v ; Payload: %+v", err.Error(), payload)
				return nil, err
			}
			sessionEvents = append(sessionEvents, sessionEvent)
		} else {
			var event models.Event
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				logger.Sugar().Errorf("SentryReceiver : Unmarshal event error: %+v ; Payload: %+v", err.Error(), payload)
				return nil, err
			}
			events = append(events, event)
		}
		break
	}

	if len(events) == 0 && len(sessionEvents) == 0 {
		return nil, fmt.Errorf("SentryReceiver : No useful payload in the envelop")
	}

	result := models.EnvelopEventParseResult{
		EnvelopTypeHeader:  type_header,
		EnvelopEventHeader: header,
		Events:             events,
		SessionEvents:      sessionEvents,
		EnvelopType:        envelopType,
	}
	return &result, nil
}
