package outbox

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

type Publisher struct {
	pool *pgxpool.Pool
	js   nats.JetStreamContext
}

func NewPublisher(pool *pgxpool.Pool, js nats.JetStreamContext) *Publisher {
	return &Publisher{pool: pool, js: js}
}

func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.publishBatch(ctx, 25); err != nil {
				log.Printf("OUTBOX: %v", err)
			}
		}
	}
}

func (p *Publisher) publishBatch(ctx context.Context, limit int) error {
	rows, err := p.pool.Query(ctx, `
        SELECT id, tenant_id, subject, payload, headers
        FROM outbox_events
        WHERE status='PENDING' AND next_attempt_at <= now()
        ORDER BY created_at
        LIMIT $1`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()

	type event struct {
		id, tenant, subject string
		payload, headers    []byte
	}
	var events []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.id, &e.tenant, &e.subject, &e.payload, &e.headers); err != nil {
			return err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, e := range events {
		msg := nats.NewMsg(e.subject)
		msg.Data = e.payload
		msg.Header.Set("Nats-Msg-Id", e.id)
		msg.Header.Set("X-ERP-Outbox-ID", e.id)
		msg.Header.Set("X-ERP-Tenant-ID", e.tenant)
		var headers map[string]string
		if len(e.headers) > 0 && json.Unmarshal(e.headers, &headers) == nil {
			for k, v := range headers {
				msg.Header.Set(k, v)
			}
		}
		if _, err := p.js.PublishMsg(msg); err != nil {
			_, _ = p.pool.Exec(ctx, `
                UPDATE outbox_events
                SET attempts=attempts+1,
                    last_error=$2,
                    next_attempt_at=now() + LEAST(interval '5 minutes', interval '2 seconds' * power(2, LEAST(attempts, 7)))
                WHERE id=$1 AND status='PENDING'`, e.id, err.Error())
			continue
		}
		_, err := p.pool.Exec(ctx, `
            UPDATE outbox_events SET status='PUBLISHED', published_at=now(), attempts=attempts+1, last_error=NULL
            WHERE id=$1 AND status='PENDING'`, e.id)
		if err != nil {
			log.Printf("OUTBOX: published %s but failed to mark published: %v", e.id, err)
		}
	}
	return nil
}
