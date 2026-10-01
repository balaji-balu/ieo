package natsbroker

import (
	"encoding/json"

	"github.com/balaji-balu/ieo/internal/gitobserver"
	"github.com/balaji-balu/ieo/pkg/model"

	//"github.com/balaji-balu/ieo/pkg/model/reconciler"

	//"github.com/balaji-balu/ieo/pkg/deployment"
	"github.com/nats-io/nats.go"
)

type Broker struct {
	conn *nats.Conn
}

func New(url string) (*Broker, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return &Broker{conn: nc}, nil
}

func (b *Broker) Publish(topic string, msg interface{}) error {
	data, _ := json.Marshal(msg)
	return b.conn.Publish(topic, data)
}

func (b *Broker) Subscribe(topic string, handler func(gitobserver.GitEvent)) error {
	_, err := b.conn.Subscribe(topic, func(m *nats.Msg) {
		var ev gitobserver.GitEvent
		_ = json.Unmarshal(m.Data, &ev)
		handler(ev)
	})
	return err
}

func (b *Broker) Subscribe2(topic string, handler func(model.HealthMsg)) error {
	_, err := b.conn.Subscribe(topic, func(m *nats.Msg) {
		var ev model.HealthMsg //gitobserver.GitEvent
		_ = json.Unmarshal(m.Data, &ev)
		handler(ev)
	})
	return err
}

func (b *Broker) Subscribe3(topic string, handler func(model.DiffOp)) error {
	_, err := b.conn.Subscribe(topic, func(m *nats.Msg) {
		var ev model.DiffOp
		_ = json.Unmarshal(m.Data, &ev)
		handler(ev)
	})
	return err
}

func (b *Broker) Subscribe4(topic string, handler func(model.DeploymentStatus)) error {
	_, err := b.conn.Subscribe(topic, func(m *nats.Msg) {
		var ev model.DeploymentStatus
		_ = json.Unmarshal(m.Data, &ev)
		handler(ev)
	})
	return err
}

// SubscribeGeneric allows subscribing with any message type
// Generic subscribe helper (Go 1.18+ compatible)
// func (b *Broker) SubscribeGeneric[T any](topic string, handler func(T)) error {
// 	_, err := b.conn.Subscribe(topic, func(m *nats.Msg) {
// 		var v T
// 		if err := json.Unmarshal(m.Data, &v); err != nil {
// 			log.Printf("❌ unmarshal error for topic %s: %v", topic, err)
// 			return
// 		}
// 		handler(v)
// 	})
// 	return err
// }

func (b *Broker) Flush() {
	b.conn.Flush()
}

func (b *Broker) Close() {
	if b.conn != nil {
		b.conn.Close()
	}
}
