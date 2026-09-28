package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Configuration change notifications (SPEC §2.3): config writes run NOTIFY <channel>,
// '<table>:<id>', and every proxy replica listens so a dashboard edit applies everywhere at once.
// A poll backs the listener up (TRUTHTABLE_POLICY_RELOAD_INTERVAL), so a lost connection only
// delays a change.

// maxChannelBytes is Postgres' identifier limit (NAMEDATALEN - 1); pg_notify refuses longer names.
const maxChannelBytes = 63

// ScopedConfigChannel returns the NOTIFY channel of an isolated schema (tests):
// "trustagent_config_<suffix>", where suffix is the end of schema, cut to fit Postgres' 63-byte
// channel limit. Schemas from storetest end in 16 random hex digits, so the suffix stays unique.
func ScopedConfigChannel(schema string) string {
	room := maxChannelBytes - len(ConfigChannel) - 1
	if len(schema) > room {
		schema = schema[len(schema)-room:]
	}
	return ConfigChannel + "_" + schema
}

// Notify sends payload on channel (ConfigChannel when empty).
func (s *PostgresStore) Notify(ctx context.Context, channel, payload string) error {
	if channel == "" {
		channel = ConfigChannel
	}
	_, err := s.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, payload)
	return err
}

// Listen subscribes to channel (ConfigChannel when empty) on a dedicated connection and calls fn
// with each notification payload until ctx ends or the connection fails. ready, when not nil, is
// called once the subscription is active. It returns ctx.Err() after a cancellation and the
// connection error otherwise; the caller decides whether to listen again. fn runs on the listening
// goroutine and should not block for long.
func (s *PostgresStore) Listen(ctx context.Context, channel string, ready func(), fn func(payload string)) error {
	if channel == "" {
		channel = ConfigChannel
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("listen: acquire: %w", err)
	}
	// A connection that has run LISTEN is never returned to the pool: closing it ends the
	// subscription, whatever state the session is in.
	defer discardConn(conn)
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return fmt.Errorf("listen %s: %w", channel, err)
	}
	if ready != nil {
		ready()
	}
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("listen %s: %w", channel, err)
		}
		if n.Channel == channel {
			fn(n.Payload)
		}
	}
}
