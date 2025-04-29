// Copyright 2024 Qubership
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
	useBulkSend ...bool,
) *GraylogSender {
	bulkSend := false
	if len(useBulkSend) > 0 {
		bulkSend = useBulkSend[0]
	}
	result := GraylogSender{
		endpoint:                    endpoint,
		logger:                      logger,
		msgQueue:                    make(chan *Message, queueSize),
		maxMessageSendRetryCnt:      maxMessageSendRetryCnt,
		maxSuccessiveSendErrCnt:     maxSuccessiveSendErrCnt,
		successiveSendErrFreezeTime: successiveSendErrFreezeTime,
		ctx:                         context.Background(),
		useBulkSend:                 bulkSend,
	}

	if bulkSend {
		result.startBatchWorker() // Start batch worker for bulk send
	} else {
		for i := 0; i < connPoolSize; i++ {
			go result.tcpConnGoroutine(i) // Send messages individually
		}
	}

	return &result
}

func (gs *GraylogSender) tcpConnGoroutine(connNumber int) {
	defer gs.logger.Sugar().Infof("GraylogTcpConnection : Goroutine #%v is finished", connNumber)
	defer func() {
		if rec := recover(); rec != nil {
			gs.logger.Sugar().Errorf("GraylogTcpConnection : Panic in goroutine #%v : %+v ; Stacktrace of the panic : %v", connNumber, rec, string(debug.Stack()))
			time.Sleep(time.Second * 5)
			gs.logger.Sugar().Infof("GraylogTcpConnection : Starting gouroutine #%v again ...", connNumber)
			go gs.tcpConnGoroutine(connNumber)
		}
	}()
	tcpAddress := fmt.Sprintf("%s:%d", gs.endpoint.Address, gs.endpoint.Port)
	gs.logger.Sugar().Infof("GraylogTcpConnection : Goroutine #%v for %v started", connNumber, tcpAddress)
	successiveGraylogErrCnt := 0
	MAX_SUCCESSIVE_SEND_ERR_CNT := gs.maxSuccessiveSendErrCnt
	messageRetryCnt := 0
	MAX_RETRY_CNT := gs.maxMessageSendRetryCnt
	FREEZE_TIME := gs.successiveSendErrFreezeTime
	var retryData *[]byte
	for {
		gs.logger.Sugar().Infof("GraylogTcpConnection : Creating tcp connection #%v to the graylog", connNumber)
		tcpConn, err := net.Dial(string(gs.endpoint.Transport), tcpAddress)
		if err != nil {
			gs.logger.Sugar().Errorf("GraylogTcpConnection : Error creating tcp connection #%v to the graylog : %+v", connNumber, err)
			time.Sleep(time.Second * 5)
			continue
		}
		for {
			var data []byte
			if messageRetryCnt > MAX_RETRY_CNT {
				gs.logger.Sugar().Errorf("GraylogTcpConnection : Message %+v is skipped after %v retries in the goroutine #%v", retryData, messageRetryCnt-1, connNumber)
				retryData = nil
			}
			if retryData != nil {
				data = *retryData
				gs.logger.Sugar().Infof("GraylogTcpConnection : Retry %v sending message in the goroutine #%v", messageRetryCnt, connNumber)
			} else {
				msg, ok := <-gs.msgQueue
				if !ok {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : Message chan is closed, stopping the goroutine #%v", connNumber)
					return
				}
				if msg == nil {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : Nil message received from chan in the goroutine #%v", connNumber)
					continue
				}
				data, err = prepareMessage(msg)
				if err != nil {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : Message %+v is skipped in the goroutine #%v, because error has happened during message preparation : %+v", msg, connNumber, err)
					continue
				}
			}
			_, err = tcpConn.Write(data)
			if err != nil {
				gs.logger.Sugar().Errorf("GraylogTcpConnection : Message %v has not been sent to the graylog, connection #%v to the graylog will be recreated : %+v\n", string(data), connNumber, err)
				err2 := tcpConn.Close()
				if err2 != nil {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : Error closing tcp connection #%v to the graylog : %+v", connNumber, err2)
				}
				retryData = &data
				messageRetryCnt++
				successiveGraylogErrCnt++
				if successiveGraylogErrCnt > MAX_SUCCESSIVE_SEND_ERR_CNT {
					gs.logger.Sugar().Errorf("GraylogTcpConnection : %v successive errors recieved from the graylog. Connection #%v is freezed for %s", successiveGraylogErrCnt, connNumber, FREEZE_TIME)
					time.Sleep(FREEZE_TIME)
				}
				break
			} else {
				messageRetryCnt = 0
				successiveGraylogErrCnt = 0
				retryData = nil
				gs.logger.Sugar().Debugf("GraylogTcpConnection : Message %+v has been sent successfully to the graylog in the goroutine #%v\n", string(data), connNumber)
			}
		}
	}
}

func (gs *GraylogSender) startBatchWorker() {
	go func() {
		var buffer strings.Builder
		for {
			msg, ok := <-gs.msgQueue
			if !ok {
				gs.logger.Sugar().Error("GraylogTcpConnection : Message chan is closed, stopping the batch worker")
				return
			}
			if msg == nil {
				gs.logger.Sugar().Error("GraylogTcpConnection : Nil message received from chan in the batch worker")
				continue
			}

			data, err := prepareMessage(msg)
			if err != nil {
				gs.logger.Sugar().Errorf("Error preparing message %v for bulk sending: %+v", msg, err)
				continue
			}
			buffer.Write(data)
			if buffer.Len() >= 1024 {
				err := gs.sendBulkMessage(buffer.String())
				if err != nil {
					gs.logger.Sugar().Errorf("Error sending bulk message: %+v", err)
				}
				buffer.Reset()
			}
		}
	}()
}

func (gs *GraylogSender) sendBulkMessage(data string) error {
	tcpAddress := fmt.Sprintf("%s:%d", gs.endpoint.Address, gs.endpoint.Port)
	tcpConn, err := net.Dial(string(gs.endpoint.Transport), tcpAddress)
	if err != nil {
		gs.logger.Sugar().Errorf("GraylogTcpConnection : Error sending bulk message: %+v", err)
		return err
	}
	defer tcpConn.Close()

	_, err = tcpConn.Write([]byte(data))
	if err != nil {
		gs.logger.Sugar().Errorf("Error writing bulk data to Graylog: %+v", err)
		return err
	}

	gs.logger.Sugar().Debug("Bulk message sent successfully to Graylog")
	return nil
}

func (gs *GraylogSender) SendRaw(data string) error {
	tcpAddress := fmt.Sprintf("%s:%d", gs.endpoint.Address, gs.endpoint.Port)
	tcpConn, err := net.Dial(string(gs.endpoint.Transport), tcpAddress)
	if err != nil {
		gs.logger.Sugar().Errorf("Error sending raw data to Graylog: %+v", err)
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
	default:
		err := fmt.Errorf("Chan is full")
		return err
	}
}

func prepareMessage(m *Message) ([]byte, error) {
	jsonMessage, err := json.Marshal(m)
	if err != nil {
		return []byte{}, err
	}

	c, err := gabs.ParseJSON(jsonMessage)
	if err != nil {
		return []byte{}, err
	}

	for key, value := range m.Extra {
		_, err = c.Set(value, fmt.Sprintf("_%s", key))
		if err != nil {
			return []byte{}, err
		}
	}

	data := append(c.Bytes(), '\n', byte(0))

	return data, nil
}
