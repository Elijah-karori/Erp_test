package eventbus

import (
	"fmt"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

type Embedded struct {
	Server *server.Server
	Conn   *nats.Conn
	JS     nats.JetStreamContext
}

func Start() (*Embedded, error) {
	opts := &server.Options{
		ServerName: "erp-eventbus",
		Host:       "127.0.0.1",
		Port:       4222,
		JetStream:  true,
		StoreDir:   "./data/jetstream",
		NoLog:      false,
		NoSigs:     true,
	}

	ns, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}

	go ns.Start()

	if !ns.ReadyForConnections(10 * time.Second) {
		return nil, fmt.Errorf("embedded nats failed to start")
	}

	nc, err := nats.Connect(
		"nats://127.0.0.1:4222",
		nats.Name("ERP"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)

	if err != nil {
		ns.Shutdown()
		return nil, err
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, err
	}

	return &Embedded{
		Server: ns,
		Conn:   nc,
		JS:     js,
	}, nil
}
