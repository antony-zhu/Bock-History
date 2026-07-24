package storage

import (
	"context"
	"errors"
	"fmt"

	"block.local/block-agent/internal/mqtt5"
)

var _ mqtt5.SessionStore = (*Store)(nil)

func (s *Store) LoadMQTTInflight(ctx context.Context) ([]mqtt5.StoredPublish, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT packet_id, topic, payload, retain, application_key, send_order, ever_sent
		FROM mqtt_outbound_inflight ORDER BY send_order ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []mqtt5.StoredPublish
	for rows.Next() {
		var (
			packetID uint64
			retain   int
			order    uint64
			everSent int
			record   mqtt5.StoredPublish
		)
		if err := rows.Scan(
			&packetID, &record.Topic, &record.Payload, &retain,
			&record.ApplicationKey, &order, &everSent,
		); err != nil {
			return nil, err
		}
		if packetID == 0 || packetID > 65535 || order == 0 ||
			(retain != 0 && retain != 1) || (everSent != 0 && everSent != 1) {
			return nil, errors.New("persisted MQTT in-flight row is invalid")
		}
		record.PacketID = uint16(packetID)
		record.Retain = retain == 1
		record.Order = order
		record.EverSent = everSent == 1
		record.Payload = append([]byte{}, record.Payload...)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) SaveMQTTInflight(
	ctx context.Context,
	record mqtt5.StoredPublish,
) error {
	if record.PacketID == 0 || record.Topic == "" || record.Order == 0 ||
		record.Order > uint64(1<<63-1) {
		return errors.New("MQTT in-flight record is invalid")
	}
	payload := record.Payload
	if payload == nil {
		payload = []byte{}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mqtt_outbound_inflight (
			packet_id, topic, payload, retain, application_key, send_order, ever_sent
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.PacketID, record.Topic, payload, boolInteger(record.Retain),
		record.ApplicationKey, record.Order, boolInteger(record.EverSent))
	if err != nil {
		return fmt.Errorf("insert MQTT in-flight row: %w", err)
	}
	return nil
}

func (s *Store) DeleteMQTTInflight(ctx context.Context, packetID uint16) error {
	if packetID == 0 {
		return errors.New("MQTT packet id is zero")
	}
	result, err := s.db.ExecContext(
		ctx, "DELETE FROM mqtt_outbound_inflight WHERE packet_id = ?", packetID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("MQTT in-flight packet %d is missing", packetID)
	}
	return nil
}

func (s *Store) ClearMQTTInflight(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM mqtt_outbound_inflight"); err != nil {
		return err
	}
	return tx.Commit()
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
