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
	defer gs.logger.Sugar().Infof("GraylogTcpConnection : Goroutine #%d finished", connNumber)

	defer func() {
		if rec := recover(); rec != nil {
			gs.logger.Sugar().Errorf("GraylogTcpConnection : Panic in goroutine #%d : %+v ; Stacktrace : %s", connNumber, rec, string(debug.Stack()))
			time.Sleep(gs.successiveSendErrFreezeTime)
			gs.logger.Sugar().Infof("GraylogTcpConnection : Restarting goroutine #%d ...", connNumber)
			go gs.tcpConnGoroutine(connNumber)
		}
	}()

	tcpAddress := net.JoinHostPort(gs.endpoint.Address, fmt.Sprintf("%d", gs.endpoint.Port))
	gs.logger.Sugar().Infof("GraylogTcpConnection : Goroutine #%d for %s started", connNumber, tcpAddress)

	var (
		successiveGraylogErrCnt = 0
		messageRetryCnt         = 0
		retryData               *[]byte
	)

	for {
		select {
		case <-gs.ctx.Done():
			gs.logger.Sugar().Infof("GraylogTcpConnection : Context canceled, stopping goroutine #%d", connNumber)
			return
		default:
		}

		gs.logger.Sugar().Infof("GraylogTcpConnection : Creating TCP connection #%d to Graylog", connNumber)
		tcpConn, err := net.Dial(string(gs.endpoint.Transport), tcpAddress)
		if err != nil {
			gs.logger.Sugar().Errorf("GraylogTcpConnection : Error creating TCP connection #%d to Graylog: %+v", connNumber, err)
			time.Sleep(gs.successiveSendErrFreezeTime)
			continue
		}

		for {
			select {
			case <-gs.ctx.Done():
				gs.logger.Sugar().Infof("GraylogTcpConnection : Context canceled, stopping goroutine #%d", connNumber)
				_ = tcpConn.Close()
				return
			default:
			}

			if messageRetryCnt > gs.maxMessageSendRetryCnt {
				gs.logger.Sugar().Errorf("GraylogTcpConnection : Message of %d bytes skipped after %d retries in goroutine #%d", payloadSize(retryData), messageRetryCnt-1, connNumber)
				retryData = nil
				messageRetryCnt = 0
			}

			var data []byte

			if retryData != nil {
				data = *retryData
				gs.logger.Sugar().Infof("GraylogTcpConnection : Retrying message send #%d in goroutine #%d", messageRetryCnt, connNumber)
			} else {
				msg, ok := <-gs.msgQueue
				if !ok {
					gs.logger.Sugar().Infof("GraylogTcpConnection : msgQueue closed, stopping goroutine #%d", connNumber)
					_ = tcpConn.Close()
					return
				}
				if msg == nil {
					gs.logger.Sugar().Warnf("GraylogTcpConnection : nil message received in goroutine #%d, skipping", connNumber)
					continue
				}

				data, err = prepareMessage(msg)
				if err != nil {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : Error preparing message from host %v with level %v and timestamp %v in goroutine #%d: %+v", msg.Host, msg.Level, msg.Timestamp, connNumber, err)
					continue
				}
			}

			gs.logger.Sugar().Debugf("GraylogTcpConnection : Sending message of %d bytes in goroutine #%d", len(data), connNumber)
			_, err = tcpConn.Write(data)
			if err != nil {
				gs.logger.Sugar().Errorf("GraylogTcpConnection : Failed to send message in goroutine #%d: %v. Closing connection and retrying...", connNumber, err)
				if errClose := tcpConn.Close(); errClose != nil {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : Error closing TCP connection #%d: %+v", connNumber, errClose)
				}
				retryData = &data
				messageRetryCnt++
				successiveGraylogErrCnt++
				if successiveGraylogErrCnt > gs.maxSuccessiveSendErrCnt {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : %d successive errors in goroutine #%d, freezing for %s", successiveGraylogErrCnt, connNumber, gs.successiveSendErrFreezeTime)
					time.Sleep(gs.successiveSendErrFreezeTime)
					successiveGraylogErrCnt = 0
				}
				break
			} else {
				messageRetryCnt = 0
				successiveGraylogErrCnt = 0
				retryData = nil
				gs.logger.Sugar().Debugf("GraylogTcpConnection : Message sent successfully in goroutine #%d", connNumber)
			}
		}
	}
}

// payloadSize returns the length in bytes of a pending GELF payload, treating a
// nil buffer as zero. It keeps message contents out of the logs.
func payloadSize(data *[]byte) int {
	if data == nil {
		return 0
	}
	return len(*data)
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
