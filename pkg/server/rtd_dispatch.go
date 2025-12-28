package server

import (
	"context"
	"github.com/xll-gen/xll-gen/pkg/log"
	"github.com/xll-gen/xll-gen/pkg/protocol"
	"github.com/xll-gen/shm/go"
)

// HandleRtdConnect parses the RtdConnectRequest and invokes the user callback.
func HandleRtdConnect(data []byte, respBuf []byte, callback func(ctx context.Context, topicID int32, strings []string, newValues bool) error) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsRtdConnectRequest(data, 0)
	topicID := reqObj.TopicId()
	newVal := reqObj.NewValues()

	var strings []string
	if reqObj.StringsLength() > 0 {
		for i := 0; i < reqObj.StringsLength(); i++ {
			strings = append(strings, string(reqObj.Strings(i)))
		}
	}

	ctx := context.Background()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Panic in OnRtdConnect", "error", r)
			}
		}()
		if err := callback(ctx, topicID, strings, newVal); err != nil {
			log.Error("OnRtdConnect failed", "error", err)
		}
	}()

	// Immediate ACK/Response
	builder := GetBuilder(respBuf)
	defer PutBuilder(builder)
	builder.Reset()
	protocol.RtdConnectResponseStart(builder)
	root := protocol.RtdConnectResponseEnd(builder)
	builder.Finish(root)
	payload := builder.FinishedBytes()

	if cap(builder.Bytes) == cap(respBuf) && len(payload) <= len(respBuf) {
		return -int32(len(payload)), 10
	}
	copy(respBuf, payload)
	return int32(len(payload)), 10
}

// HandleRtdDisconnect parses the RtdDisconnectRequest and invokes the user callback.
func HandleRtdDisconnect(data []byte, respBuf []byte, callback func(ctx context.Context, topicID int32) error) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsRtdDisconnectRequest(data, 0)
	topicID := reqObj.TopicId()

	// Auto-unsubscribe from GlobalRtd
	GlobalRtd.Unsubscribe(topicID)

	ctx := context.Background()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("Panic in OnRtdDisconnect", "error", r)
			}
		}()
		if err := callback(ctx, topicID); err != nil {
			log.Error("OnRtdDisconnect failed", "error", err)
		}
	}()
	return 0, 0
}
