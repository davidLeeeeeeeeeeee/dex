package network

import (
	"context"
	"dex/logs"
	"sync"
)

// MessageQueue 消息队列（替代全局sender.Queue）
type MessageQueue struct {
	queue chan *Message
	ctx   context.Context
	wg    sync.WaitGroup
}

type Message struct {
	To      string
	Payload interface{}
	Retry   int
}

func NewMessageQueue(size int) *MessageQueue {
	return &MessageQueue{
		queue: make(chan *Message, size),
		ctx:   context.Background(),
	}
}

func (mq *MessageQueue) Start(network *Network) {
	mq.wg.Add(1)
	go mq.processLoop(network)
}

func (mq *MessageQueue) Stop() {
	close(mq.queue)
	mq.wg.Wait()
}

func (mq *MessageQueue) Enqueue(msg *Message) {
	select {
	case mq.queue <- msg:
	default:
		logs.Warn("Message queue is full, dropping message")
	}
}

func (mq *MessageQueue) processLoop(network *Network) {
	defer mq.wg.Done()

	for msg := range mq.queue {
		if err := network.SendMessage(msg.To, msg.Payload); err != nil {
			if msg.Retry > 0 {
				msg.Retry--
				mq.Enqueue(msg)
			} else {
				logs.Error("Failed to send message after retries: %v", err)
			}
		}
	}
}
