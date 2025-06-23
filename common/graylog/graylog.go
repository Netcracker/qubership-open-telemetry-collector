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
	"strings"
	"time"

	"github.com/Jeffail/gabs"
	"go.uber.org/zap"
)

type Transport string

const (
	UDP                      Transport = "udp"
	TCP                      Transport = "tcp"
	batchWorkerFlushInterval           = 5 * time.Second
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
	useBulkSend                 bool
}

type Message struct {
	Version      string            `json:"version"`
	Host         string            `json:"host"`
	ShortMessage string            `json:"short-message"`
	FullMessage  string            `json:"full-message,omitempty"`
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
	useBulkSend ...bool,
) *GraylogSender {

	bulkSend := false
	if len(useBulkSend) > 0 {
		bulkSend = useBulkSend[0]
	}

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
		useBulkSend:                 bulkSend,
	}
	gs.logger.Info("GraylogSender initialized")
	if bulkSend {
		gs.logger.Info("GraylogSender starting in bulk send mode")
		gs.startBatchWorker(queueSize)
	} else {
		gs.logger.Info("GraylogSender starting in individual send mode")
		for i := 0; i < connPoolSize; i++ {
			go gs.tcpConnGoroutine(i)
		}
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

	tcpAddress := fmt.Sprintf("%s:%d", gs.endpoint.Address, gs.endpoint.Port)
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
				gs.logger.Sugar().Errorf("GraylogTcpConnection : Message %+v skipped after %d retries in goroutine #%d", retryData, messageRetryCnt-1, connNumber)
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
					gs.logger.Sugar().Errorf("GraylogTcpConnection : Error preparing message %+v in goroutine #%d: %+v", msg, connNumber, err)
					continue
				}
			}

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

func (gs *GraylogSender) startBatchWorker(batch int) {
	go func() {
		var buffer strings.Builder
		ticker := time.NewTicker(batchWorkerFlushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-gs.ctx.Done():
				gs.logger.Info("GraylogBatchWorker : context canceled, stopping worker")
				return

			case msg, ok := <-gs.msgQueue:
				if !ok {
					gs.logger.Info("GraylogBatchWorker : msgQueue closed, stopping worker")
					return
				}
				if msg == nil {
					gs.logger.Warn("GraylogBatchWorker : nil message received, skipping")
					continue
				}

				data, err := prepareMessage(msg)
				if err != nil {
					gs.logger.Sugar().Errorf("GraylogBatchWorker : error preparing message for bulk send: %+v", err)
					continue
				}

				buffer.Write(data)
				buffer.WriteByte(0x00)

				if buffer.Len() >= batch {
					if err := gs.SendRaw(buffer.String()); err != nil {
						gs.logger.Sugar().Errorf("GraylogBatchWorker : error sending bulk message: %+v", err)
					}
					buffer.Reset()
				}

			case <-ticker.C:
				if buffer.Len() > 0 {
					if err := gs.SendRaw(buffer.String()); err != nil {
						gs.logger.Sugar().Errorf("GraylogBatchWorker : error sending bulk message (timer flush): %+v", err)
					}
					buffer.Reset()
				}
			}
		}
	}()
}

func (gs *GraylogSender) SendRaw(data string) error {
	tcpAddress := fmt.Sprintf("%s:%d", gs.endpoint.Address, gs.endpoint.Port)
	tcpConn, err := net.Dial(string(gs.endpoint.Transport), tcpAddress)
	if err != nil {
		gs.logger.Sugar().Errorf("Error dialing Graylog TCP at %s: %+v", tcpAddress, err)
		return err
	}
	defer tcpConn.Close()

	_, err = tcpConn.Write([]byte(data))
	if err != nil {
		gs.logger.Sugar().Errorf("Error writing raw data to Graylog: %+v", err)
		return err
	}

	gs.logger.Sugar().Debug("Raw data sent successfully to Graylog")
	return nil
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
	jsonMessage, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	c, err := gabs.ParseJSON(jsonMessage)
	if err != nil {
		return nil, err
	}

	for key, value := range m.Extra {
		_, err = c.Set(value, fmt.Sprintf("_%s", key))
		if err != nil {
			return nil, err
		}
	}

	data := append(c.Bytes(), '\n', byte(0))
	return data, nil
}
