package endpoint

import (
	"fmt"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/weaveworks/procspy"
	"github.com/weaveworks/scope/report"
)

// Reporter generates Reports containing the Endpoint topology.
type Reporter struct {
	firstSeenTimes   map[string]time.Time
	hostID           string
	hostName         string
	includeNAT       bool
	includeProcesses bool
}

// SpyDuration is an exported prometheus metric
var SpyDuration = prometheus.NewSummaryVec(
	prometheus.SummaryOpts{
		Namespace: "scope",
		Subsystem: "probe",
		Name:      "spy_time_nanoseconds",
		Help:      "Total time spent spying on active connections.",
		MaxAge:    10 * time.Second, // like statsd
	},
	[]string{},
)

// NewReporter creates a new Reporter that invokes procspy.Connections to
// generate a report.Report that contains every discovered (spied) connection
// on the host machine, at the granularity of host and port. That information
// is stored in the Endpoint topology. It optionally enriches that topology
// with process (PID) information.
func NewReporter(hostID, hostName string, includeProcesses bool) *Reporter {
	return &Reporter{
		firstSeenTimes:   map[string]time.Time{},
		hostID:           hostID,
		hostName:         hostName,
		includeNAT:       conntrackModulePresent(),
		includeProcesses: includeProcesses,
	}
}

// Report implements Reporter.
func (r *Reporter) Report() (report.Report, error) {
	now := time.Now()
	defer func(begin time.Time) {
		SpyDuration.WithLabelValues().Observe(float64(time.Since(begin)))
	}(now)

	rpt := report.MakeReport()
	conns, err := procspy.Connections(r.includeProcesses)
	if err != nil {
		return rpt, err
	}

	for conn := conns.Next(); conn != nil; conn = conns.Next() {
		r.addConnection(&rpt, conn, now)
	}

	r.cleanupConnections(&rpt)

	if r.includeNAT {
		err = applyNAT(rpt, r.hostID)
	}

	return rpt, err
}

func (r *Reporter) addConnection(rpt *report.Report, c *procspy.Connection, firstSeen time.Time) {
	var (
		scopedLocal  = report.MakeAddressNodeID(r.hostID, c.LocalAddress.String())
		scopedRemote = report.MakeAddressNodeID(r.hostID, c.RemoteAddress.String())
		key          = report.MakeAdjacencyID(scopedLocal)
		edgeKey      = report.MakeEdgeID(scopedLocal, scopedRemote)
	)
	firstSeen = r.lookupFirstSeenTime(edgeKey, firstSeen)

	rpt.Address.Adjacency[key] = rpt.Address.Adjacency[key].Add(scopedRemote, firstSeen)

	if _, ok := rpt.Address.NodeMetadatas[scopedLocal]; !ok {
		rpt.Address.NodeMetadatas[scopedLocal] = report.NodeMetadata{
			"name": r.hostName,
			"addr": c.LocalAddress.String(),
		}
	}

	countTCPConnection(rpt.Address.EdgeMetadatas, edgeKey)

	if c.Proc.PID > 0 {
		var (
			scopedLocal  = report.MakeEndpointNodeID(r.hostID, c.LocalAddress.String(), strconv.Itoa(int(c.LocalPort)))
			scopedRemote = report.MakeEndpointNodeID(r.hostID, c.RemoteAddress.String(), strconv.Itoa(int(c.RemotePort)))
			key          = report.MakeAdjacencyID(scopedLocal)
			edgeKey      = report.MakeEdgeID(scopedLocal, scopedRemote)
		)
		firstSeen = r.lookupFirstSeenTime(edgeKey, firstSeen)

		rpt.Endpoint.Adjacency[key] = rpt.Endpoint.Adjacency[key].Add(scopedRemote, firstSeen)

		if _, ok := rpt.Endpoint.NodeMetadatas[scopedLocal]; !ok {
			// First hit establishes NodeMetadata for scoped local address + port
			md := report.NodeMetadata{
				"addr": c.LocalAddress.String(),
				"port": strconv.Itoa(int(c.LocalPort)),
				"pid":  fmt.Sprintf("%d", c.Proc.PID),
			}

			rpt.Endpoint.NodeMetadatas[scopedLocal] = md
		}

		countTCPConnection(rpt.Endpoint.EdgeMetadatas, edgeKey)
	}
}

func countTCPConnection(m report.EdgeMetadatas, edgeKey string) {
	edgeMeta := m[edgeKey]
	edgeMeta.WithConnCountTCP = true
	edgeMeta.MaxConnCountTCP++
	m[edgeKey] = edgeMeta
}

func (r *Reporter) lookupFirstSeenTime(edgeKey string, firstSeen time.Time) time.Time {
	if t, ok := r.firstSeenTimes[edgeKey]; ok {
		firstSeen = t
	} else {
		r.firstSeenTimes[edgeKey] = firstSeen
	}
	return firstSeen
}

func (r *Reporter) cleanupConnections(rpt *report.Report) {
	missingKeys := []string{}
	for edgeKey, _ := range r.firstSeenTimes {
		if _, ok := rpt.Address.EdgeMetadatas[edgeKey]; !ok {
			continue
		}
		if _, ok := rpt.Endpoint.EdgeMetadatas[edgeKey]; !ok {
			continue
		}
		missingKeys = append(missingKeys, edgeKey)
	}
	for _, edgeKey := range missingKeys {
		delete(r.firstSeenTimes, edgeKey)
	}
}
