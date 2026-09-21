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

package graylog

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"runtime/debug"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Transport string

const (
	UDP Transport = "udp"
	TCP Transport = "tcp"
)

type Endpoint struct {
	Transport Transport
	Address   string
	Port      uint
}

type GraylogSender struct {
	ctx                         context.Context
	cancel                      context.CancelFunc
	endpoint                    Endpoint
	msgQueue                    chan *Message
	logger                      *zap.Logger
	maxMessageSendRetryCnt      int
	maxSuccessiveSendErrCnt     int
	successiveSendErrFreezeTime time.Duration
}

type Message struct {
	Version      string            `json:"version"`
	Host         string            `json:"host"`
	ShortMessage string            `json:"short_message"`
	FullMessage  string            `json:"full_message,omitempty"`
	Timestamp    int64             `json:"timestamp,omitempty"`
	Level        uint              `json:"level,omitempty"`
	Extra        map[string]string `json:"-"`
}

func NewGraylogSender(
	endpoint Endpoint,
	logger *zap.Logger,
	connPoolSize int,
	queueSize int,
	maxMessageSendRetryCnt int,
	maxSuccessiveSendErrCnt int,
	successiveSendErrFreezeTime time.Duration,
) *GraylogSender {

	ctx, cancel := context.WithCancel(context.Background())

	gs := &GraylogSender{
		ctx:                         ctx,
		cancel:                      cancel,
		endpoint:                    endpoint,
		logger:                      logger,
		msgQueue:                    make(chan *Message, queueSize),
		maxMessageSendRetryCnt:      maxMessageSendRetryCnt,
		maxSuccessiveSendErrCnt:     maxSuccessiveSendErrCnt,
		successiveSendErrFreezeTime: successiveSendErrFreezeTime,
	}
	gs.logger.Info("GraylogSender initialized")
	for i := 0; i < connPoolSize; i++ {
		go gs.tcpConnGoroutine(i)
	}

	return gs
}

func (gs *GraylogSender) Stop() {
	gs.logger.Info("GraylogSender stopping...")
	gs.cancel()
	close(gs.msgQueue)
}

func (gs *GraylogSender) tcpConnGoroutine(connNumber int) {
	logger := gs.logger.Named("graylogtcpconnection").With(zap.Int("goroutine_number", connNumber))

	defer logger.Info("Goroutine finished")

	defer func() {
		if rec := recover(); rec != nil {
			// The collector core leaves DisableStacktrace false, so zap appends its own
			// "stacktrace" key on every Error entry. Raise the automatic threshold here so
			// the panic stack supplied below is the only "stacktrace" field on the line.
			logger.WithOptions(zap.AddStacktrace(zapcore.FatalLevel)).Error("Panic in goroutine",
				zap.Any("error", rec),
				zap.String("stacktrace", string(debug.Stack())))
			time.Sleep(gs.successiveSendErrFreezeTime)
			logger.Info("Restarting goroutine")
			go gs.tcpConnGoroutine(connNumber)
		}
	}()

	tcpAddress := net.JoinHostPort(gs.endpoint.Address, fmt.Sprintf("%d", gs.endpoint.Port))
	logger.Info("Goroutine started", zap.String("tcp_address", tcpAddress))

	var (
		successiveGraylogErrCnt = 0
		messageRetryCnt         = 0
		retryData               *[]byte
	)

	for {
		select {
		case <-gs.ctx.Done():
			logger.Info("Context canceled, stopping goroutine")
			return
		default:
		}

		logger.Info("Creating TCP connection to Graylog")
		tcpConn, err := net.Dial(string(gs.endpoint.Transport), tcpAddress)
		if err != nil {
			logger.Error("Error creating TCP connection to Graylog", zap.Error(err))
			time.Sleep(gs.successiveSendErrFreezeTime)
			continue
		}

		for {
			select {
			case <-gs.ctx.Done():
				logger.Info("Context canceled, stopping goroutine")
				_ = tcpConn.Close()
				return
			default:
			}

			if messageRetryCnt > gs.maxMessageSendRetryCnt {
				logger.Error("Message skipped after retries",
					zap.Any("dropped_message", retryData),
					zap.Int("retry_count", messageRetryCnt-1))
				retryData = nil
				messageRetryCnt = 0
			}

			var data []byte

			if retryData != nil {
				data = *retryData
				logger.Info("Retrying message send", zap.Int("retry_count", messageRetryCnt))
			} else {
				msg, ok := <-gs.msgQueue
				if !ok {
					logger.Info("msgQueue closed, stopping goroutine")
					_ = tcpConn.Close()
					return
				}
				if msg == nil {
					logger.Warn("nil message received, skipping")
					continue
				}

				data, err = prepareMessage(msg)
				if err != nil {
					logger.Error("Error preparing message",
						zap.Any("graylog_message", msg),
						zap.Error(err))
					continue
				}
			}

			gs.logger.Sugar().Debugf("GraylogTcpConnection : Sending message in goroutine #%d: %s", connNumber, string(data))
			_, err = tcpConn.Write(data)
			if err != nil {
				logger.Error("Failed to send message, closing connection and retrying", zap.Error(err))
				if errClose := tcpConn.Close(); errClose != nil {
					logger.Error("Error closing TCP connection", zap.Error(errClose))
				}
				retryData = &data
				messageRetryCnt++
				successiveGraylogErrCnt++
				if successiveGraylogErrCnt > gs.maxSuccessiveSendErrCnt {
					logger.Error("Successive send errors, freezing goroutine",
						zap.Int("successive_error_count", successiveGraylogErrCnt),
						zap.Duration("freeze_time", gs.successiveSendErrFreezeTime))
					time.Sleep(gs.successiveSendErrFreezeTime)
					successiveGraylogErrCnt = 0
				}
				break
			} else {
				messageRetryCnt = 0
				successiveGraylogErrCnt = 0
				retryData = nil
				logger.Debug("Message sent successfully")
			}
		}
	}
}

func (gs *GraylogSender) SendToQueue(m *Message) error {
	select {
	case gs.msgQueue <- m:
		return nil
	case <-gs.ctx.Done():
		return fmt.Errorf("sender stopped")
	default:
		return fmt.Errorf("message queue is full")
	}
}

func prepareMessage(m *Message) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("message cannot be nil")
	}

	jsonMessage, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message to JSON: %w", err)
	}
	data, err := addExtraFields(jsonMessage, m.Extra)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] != 0 {
		data = append(data, 0)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("final message contains invalid UTF-8 characters")
	}
	return data, nil
}

func addExtraFields(jsonMessage []byte, extra map[string]string) ([]byte, error) {
	rawPayload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(jsonMessage, &rawPayload); err != nil {
		return nil, fmt.Errorf("failed to decode message JSON: %w", err)
	}

	payload := make(map[string]any)
	for key, value := range rawPayload {
		payload[key] = value
	}
	for key, value := range extra {
		payload["_"+key] = value
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal final message: %w", err)
	}
	return data, nil
}
