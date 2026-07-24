package mqtt5

import "context"

// StoredPublish is the durable MQTT transport state required to resume an
// outbound QoS 1 packet after a process restart.
type StoredPublish struct {
	PacketID       uint16
	Topic          string
	Payload        []byte
	Retain         bool
	ApplicationKey string
	Order          uint64
	EverSent       bool
}

// SessionStore persists MQTT transport in-flight state. Application-level
// reliable data remains independently owned by the SQLite Outbox.
type SessionStore interface {
	LoadMQTTInflight(context.Context) ([]StoredPublish, error)
	SaveMQTTInflight(context.Context, StoredPublish) error
	DeleteMQTTInflight(context.Context, uint16) error
	ClearMQTTInflight(context.Context) error
}
