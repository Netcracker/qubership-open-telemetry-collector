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
	"testing"
)

func TestEventExceptionUnmarshalValuesObject(t *testing.T) {
	payload := []byte(`{
		"exception": {
			"values": [{
				"type": "Error",
				"value": "wrapped",
				"module": "app",
				"thread_id": 7,
				"mechanism": {
					"type": "generic",
					"handled": true,
					"exception_id": 1,
					"parent_id": 0,
					"is_exception_group": true,
					"source": "errors[0]"
				}
			}]
		}
	}`)

	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}

	if got := len(event.Exception.Values); got != 1 {
		t.Fatalf("expected 1 exception value, got %d", got)
	}

	value := event.Exception.Values[0]
	if value.Type != "Error" || value.Module != "app" || value.Mechanism.Source != "errors[0]" {
		t.Fatalf("unexpected exception value: %+v", value)
	}
	if value.Mechanism.ExceptionID == nil || *value.Mechanism.ExceptionID != 1 {
		t.Fatalf("unexpected exception_id: %+v", value.Mechanism.ExceptionID)
	}
	if value.Mechanism.ParentID == nil || *value.Mechanism.ParentID != 0 {
		t.Fatalf("unexpected parent_id: %+v", value.Mechanism.ParentID)
	}
	if !value.Mechanism.IsExceptionGroup {
		t.Fatal("expected exception group flag")
	}
}

func TestEventExceptionUnmarshalFlatValuesList(t *testing.T) {
	payload := []byte(`{
		"exception": [{
			"type": "TypeError",
			"value": "flat exception"
		}]
	}`)

	var event Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}

	if got := len(event.Exception.Values); got != 1 {
		t.Fatalf("expected 1 exception value, got %d", got)
	}
	if event.Exception.Values[0].Type != "TypeError" {
		t.Fatalf("unexpected exception value: %+v", event.Exception.Values[0])
	}
}
