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

package utils

import (
	"bytes"
	"flag"
	"fmt"
	"sort"
	"strings"
)

const (
	SELF_METRICS_REGISTRY_NAME = "__SELF_METRICS__"
)

var (
	DisableTimestamp  = flag.Bool("disable-timestamp", false, "If set to true, prometheus output does not include timestamp. By default timestamp is included to prometheus output")
	croniterPrecision = flag.String("croniter-precision", "second", "Croniter precision, possible values: second, minute")
)

func MapToString(m map[string]string) string {
	keys := make([]string, len(m))
	i := 0
	for k := range m {
		keys[i] = k
		i++
	}
	sort.Strings(keys)

	b := new(bytes.Buffer)

	for _, key := range keys {
		fmt.Fprintf(b, "%s=\"%s\",", key, m[key])
	}

	return b.String()
}

func GetKeys(m map[string]string) []string {
	result := make([]string, len(m))
	i := 0
	for key := range m {
		result[i] = key
		i++
	}
	return result
}

func GetOrderedMapValues(m map[string]string, keys []string) []string {
	values := make([]string, len(keys))
	for i, key := range keys {
		values[i] = m[key]
	}
	return values
}

func GetOrderedMapValuesFloat64Uint64(m map[float64]uint64, keys []float64) []uint64 {
	values := make([]uint64, len(keys))
	for i, key := range keys {
		values[i] = m[key]
	}
	return values
}

func FindStringIndexInArray(arr []string, searchedString string) int {
	for i, s := range arr {
		if s == searchedString {
			return i
		}
	}
	return -1
}

func GetAverage(arr []float64) float64 {
	size := len(arr)
	if size == 0 {
		return 0.0
	}
	sum := 0.0
	for _, val := range arr {
		sum += val
	}
	return sum / float64(size)
}

func RemoveIDsFromURI(uri string, uuidReplacer string, numberReplacer string, idReplacer string, idDigitQuantity int, fsmReplacer string, fsmLimit int) string {
	elements := strings.Split(uri, "/")
	for i := range elements {
		if uuidReplacer != "" && isUUID(elements[i]) {
			elements[i] = uuidReplacer
			continue
		}
		if numberReplacer != "" && isNumber(elements[i]) {
			elements[i] = numberReplacer
			continue
		}
		if idReplacer != "" && IsID(elements[i], idDigitQuantity) {
			elements[i] = idReplacer
			continue
		}
		if fsmReplacer != "" && IsIdFSM(elements[i], fsmLimit) {
			elements[i] = fsmReplacer
		}
	}

	return strings.Join(elements, "/")
}

func isUUID(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
}

func isNumber(s string) bool {
	if len(s) < 1 {
		return false
	}
	if s[0] == '-' || s[0] == '+' {
		s = s[1:]
	}
	if len(s) < 1 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func IsID(s string, idDigitQuantity int) bool {
	counter := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			counter++
			if counter >= idDigitQuantity {
				return true
			}
		}
	}
	return false
}

const (
	START      = 0
	LOWER_CASE = 1
	UPPER_CASE = 2
	DIGIT      = 3
	DELIMITER  = 4
	OTHER      = 5
)

func IsIdFSM(s string, limit int) bool {
	var state int = START
	var counter int = 0
	var digitsAndOtherCounter = 0
	for _, c := range s {
		if c >= 'a' && c <= 'z' {
			if state == LOWER_CASE {
				continue
			}
			if state == UPPER_CASE {
				counter++
			} else if state == DIGIT || state == OTHER {
				counter += 2
			}
			state = LOWER_CASE
		} else if c >= 'A' && c <= 'Z' {
			if state == LOWER_CASE {
				counter++
			} else if state == DIGIT || state == OTHER {
				counter += 2
			}
			state = UPPER_CASE
		} else if c >= '0' && c <= '9' {
			digitsAndOtherCounter++
			if state == UPPER_CASE || state == LOWER_CASE || state == DELIMITER {
				counter++
			} else if state == DIGIT {
				counter += 2
				continue
			} else if state == START {
				counter += 5
			} else {
				counter += 3
			}
			state = DIGIT
		} else if c == '-' || c == '_' || c == '.' {
			if state == LOWER_CASE || state == UPPER_CASE {
				state = DELIMITER
				continue
			}
			if state == DIGIT {
				counter++
			} else if state == START || state == OTHER {
				counter += 3
			} else if state == DELIMITER {
				counter += 2
			}
			state = DELIMITER
		} else {
			counter += 3
			digitsAndOtherCounter++
			state = OTHER
		}
	}
	if state == OTHER || state == DELIMITER {
		counter++
	}
	if digitsAndOtherCounter == 0 {
		counter -= 5
	}
	size := len(s)
	if size >= 16 && size%4 == 0 {
		counter++
	}
	if counter >= limit {
		return true
	}
	return false
}
