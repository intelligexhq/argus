package collector

import (
	"context"
	"time"

	"github.com/intelligexhq/argus/internal/classify"
	"github.com/intelligexhq/argus/internal/model"
	gnet "github.com/shirou/gopsutil/v3/net"
)

// NetworkCollector maps processes to their outbound connections and classifies
// the remote endpoint. Egress to a known model endpoint is the strongest signal
// for an *unknown* agent (one whose binary name we don't recognise).
//
// Limitation: the socket table gives a remote IP, not a hostname. Robust model-
// endpoint identification wants DNS/SNI capture (a future collector). For now we
// classify network locality directly and best-effort reverse-DNS for labels.
type NetworkCollector struct {
	classifier *classify.EndpointClassifier
}

func NewNetworkCollector(c *classify.EndpointClassifier) *NetworkCollector {
	return &NetworkCollector{classifier: c}
}

func (c *NetworkCollector) Name() string { return "network" }

func (c *NetworkCollector) Collect(ctx context.Context) (Result, error) {
	conns, err := gnet.ConnectionsWithContext(ctx, "inet")
	if err != nil {
		return Result{}, err
	}
	now := time.Now()
	out := make([]model.Connection, 0, len(conns))
	for _, cn := range conns {
		if cn.Pid == 0 || cn.Raddr.IP == "" || cn.Raddr.Port == 0 {
			continue // listening / local / unattributed socket
		}
		label, host, class := c.classifier.Classify(ctx, cn.Raddr.IP)
		out = append(out, model.Connection{
			PID:            cn.Pid,
			RemoteIP:       cn.Raddr.IP,
			RemoteHost:     host, // raw PTR result, possibly generic (*.cloudfront.net) or ""
			RemotePort:     cn.Raddr.Port,
			Endpoint:       label,
			Classification: class,
			ObservedAt:     now,
			Source:         "socket",
		})
	}
	return Result{Connections: out}, nil
}
