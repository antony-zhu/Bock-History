package agent

import (
	"context"
	"fmt"
	"time"

	"block.local/block-agent/internal/alarmhistory"
	"block.local/block-agent/internal/mqttv2"
	"block.local/block-agent/internal/pointstore"
)

func (r *Runtime) newMQTTSession() (*mqttv2.Session, context.Context, context.CancelFunc, error) {
	if !r.mqtt.Enabled {
		return nil, nil, nil, nil
	}
	if r.alarms == nil {
		return nil, nil, nil, fmt.Errorf("MQTTS v2 requires local alarm history storage")
	}
	session, err := mqttv2.NewSession(r.mqtt.Connection, r.alarms, mqttv2.Options{Now: r.now})
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return session, ctx, cancel, nil
}

func detachMQTTLocked(session *runtimeSession) context.CancelFunc {
	if session == nil {
		return nil
	}
	cancel := session.mqttCancel
	session.mqtt = nil
	session.mqttCancel = nil
	return cancel
}

type runtimeAlarmNotifier struct{ runtime *Runtime }

func (n runtimeAlarmNotifier) Notify(ctx context.Context, record alarmhistory.Record) error {
	n.runtime.mu.Lock()
	var mqtt *mqttv2.Session
	if n.runtime.session != nil {
		mqtt = n.runtime.session.mqtt
	}
	n.runtime.mu.Unlock()
	if mqtt == nil {
		return nil
	}
	return mqtt.Notify(ctx, record)
}

func (r *Runtime) recordAlarmChanges(session *runtimeSession, changed map[string]pointstore.PointValue) error {
	if r.alarms == nil || session == nil {
		return nil
	}
	records := make([]alarmhistory.Record, 0)
	r.mu.Lock()
	if r.session != session {
		r.mu.Unlock()
		return nil
	}
	if session.alarms == nil {
		session.alarms = make(map[string]bool)
	}
	for pointID, value := range changed {
		if value.Quality != "good" || value.AlarmActive == nil {
			continue
		}
		definition, exists := r.store.Definition(pointID)
		if !exists || definition.Alarm == nil {
			continue
		}
		active := *value.AlarmActive
		previous, known := session.alarms[pointID]
		session.alarms[pointID] = active
		if (!known && !active) || (known && previous == active) {
			continue
		}
		eventKind := "CLEARED"
		if active {
			eventKind = "RAISED"
		}
		counter := r.alarmID.Add(1)
		records = append(records, alarmhistory.Record{
			AlarmRecordID: fmt.Sprintf("%s-%d-%d", pointID, r.now().UTC().UnixNano(), counter),
			AlarmID:       pointID,
			EventKind:     eventKind,
			Code:          pointID,
			Severity:      "warning",
			Text:          definition.Alarm.Message,
			OccurredAt:    value.UpdatedAt.UTC(),
			Details:       map[string]any{"pointId": pointID, "value": value.Value},
		})
	}
	r.mu.Unlock()
	for _, record := range records {
		if _, err := r.alarms.Append(context.Background(), record); err != nil {
			return err
		}
	}
	return nil
}

func toMQTTSnapshot(values map[string]pointstore.PointValue, now func() time.Time) mqttv2.Snapshot {
	converted := make(map[string]mqttv2.PointValue, len(values))
	for pointID, value := range values {
		converted[pointID] = mqttv2.PointValue{
			Value: value.Value, Quality: mqttv2.Quality(value.Quality),
			UpdatedAt: value.UpdatedAt, AlarmActive: value.AlarmActive,
		}
	}
	return mqttv2.Snapshot{Values: converted, StateAt: now().UTC()}
}
