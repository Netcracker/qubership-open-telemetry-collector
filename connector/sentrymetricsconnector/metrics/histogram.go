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

package metrics

import (
	"sync"

	"github.com/Netcracker/qubership-open-telemetry-collector/utils"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

type CustomHistogram struct {
	sync.RWMutex
	stateMap map[string]*CurrentHistogramState
	logger   *zap.Logger
}

type CurrentHistogramState struct {
	Sum        float64
	Cnt        uint64
	Buckets    map[float64]uint64
	BucketList []float64
	Labels     map[string]string
}

func NewCustomHistogram(logger *zap.Logger) *CustomHistogram {
	customHistogram := CustomHistogram{}
	customHistogram.stateMap = make(map[string]*CurrentHistogramState)
	customHistogram.logger = logger
	return &customHistogram
}

func (h *CustomHistogram) ObserveSingle(val float64, bucketList []float64, labels map[string]string) {
	h.Lock()
	defer h.Unlock()
	histKey := utils.MapToString(labels)
	if h.stateMap[histKey] == nil {
		histState := &CurrentHistogramState{
			Sum:        0,
			Cnt:        0,
			Labels:     labels,
			BucketList: bucketList,
			Buckets:    make(map[float64]uint64),
		}
		for _, b := range bucketList {
			histState.Buckets[b] = 0
		}
		h.stateMap[histKey] = histState
	}

	h.stateMap[histKey].Sum += val
	h.stateMap[histKey].Cnt += 1
	for _, b := range bucketList {
		if val <= b {
			h.stateMap[histKey].Buckets[b]++
			break
		}
	}
}

func (h *CustomHistogram) UpdateDataPoints(metric pmetric.Metric) {
	h.Lock()
	defer h.Unlock()
	metric.SetName("sentry_measurements_statistic")
	metric.SetDescription("The metric shows sentry measurements statistic")
	metric.SetUnit("millisecond")
	hist := metric.SetEmptyHistogram()
	hist.SetAggregationTemporality(2)
	dataPoints := hist.DataPoints()

	for _, v := range h.stateMap {
		dataPoint := dataPoints.AppendEmpty()
		dataPoint.SetSum(v.Sum)
		dataPoint.SetCount(v.Cnt)
		dataPoint.ExplicitBounds().FromRaw(v.BucketList)
		dataPoint.BucketCounts().FromRaw(utils.GetOrderedMapValuesFloat64Uint64(v.Buckets, v.BucketList))
		for label, labelValue := range v.Labels {
			dataPoint.Attributes().PutStr(label, labelValue)
		}
	}
}
