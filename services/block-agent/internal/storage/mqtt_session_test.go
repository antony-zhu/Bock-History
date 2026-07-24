package storage

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"block.local/block-agent/internal/mqtt5"
)

func TestMQTTSessionStorePreservesExactOrderedInflightState(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "block.db")
	store, err := Open(databasePath, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	ctx := context.Background()
	second := mqtt5.StoredPublish{
		PacketID: 9, Topic: "topic/second", Payload: []byte{0x00, 0xff, 0x01},
		Retain: true, ApplicationKey: "outbox:second", Order: 2, EverSent: true,
	}
	first := mqtt5.StoredPublish{
		PacketID: 7, Topic: "topic/first", Payload: []byte(`{"exact":true}`),
		ApplicationKey: "outbox:first", Order: 1, EverSent: true,
	}
	for _, record := range []mqtt5.StoredPublish{second, first} {
		if err := store.SaveMQTTInflight(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = Open(databasePath, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadMQTTInflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 ||
		loaded[0].PacketID != first.PacketID || loaded[1].PacketID != second.PacketID ||
		!bytes.Equal(loaded[0].Payload, first.Payload) ||
		!bytes.Equal(loaded[1].Payload, second.Payload) ||
		!loaded[1].Retain || loaded[1].ApplicationKey != second.ApplicationKey {
		t.Fatalf("loaded MQTT in-flight records = %+v", loaded)
	}
	if err := store.DeleteMQTTInflight(ctx, first.PacketID); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadMQTTInflight(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].PacketID != second.PacketID {
		t.Fatalf("after PUBACK delete = %+v, err=%v", loaded, err)
	}
	if err := store.ClearMQTTInflight(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.LoadMQTTInflight(ctx)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("after broker session loss clear = %+v, err=%v", loaded, err)
	}
}
