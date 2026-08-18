package collector

import (
	"context"
	"database/sql"
	"log/slog"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// subsystem
	slaveThreads = "slave_threads"
)

var (
	slaveThreadNames  = []string{"thread/sql/slave_io", "thread/sql/slave_sql", "thread/sql/rpl_parallel", "thread/sql/rpl_parallel_thread", "thread/sql/replica_io", "thread/sql/replica_sql", "thread/sql/replica_worker", "thread/sql/slave_worker"}
	slaveThreadsQuery = `
		SELECT t.THREAD_ID, t.NAME, t.PROCESSLIST_STATE
		FROM performance_schema.threads t
		WHERE t.NAME IN ('` + strings.Join(slaveThreadNames, "', '") + `')
	`

	idleThreadStatesRegex = regexp.MustCompile("(?i)(Waiting for ((source|master) (to send event|update)|more updates|an event from Coordinator|work from (main )?SQL threads?)|Slave has read all relay log)")
)

// Metric descriptors.
var (
	slaveThreadsInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, slaveThreads, "busy_state"),
		"Busy state of running slave threads",
		[]string{"thread_id", "name"}, nil,
	)
)

// ScrapeSlaveHosts scrapes metrics about the replicating slaves.
type ScrapeSlaveThreads struct{}

// Name of the Scraper. Should be unique.
func (ScrapeSlaveThreads) Name() string {
	return slaveThreads
}

// Help describes the role of the Scraper.
func (ScrapeSlaveThreads) Help() string {
	return "Collect information about slave threads"
}

// Version of MySQL from which scraper is available.
func (ScrapeSlaveThreads) Version() float64 {
	return 5.6
}

// Scrape collects data from database connection and sends it over channel as prometheus metric.
func (ScrapeSlaveThreads) Scrape(ctx context.Context, instance *instance, ch chan<- prometheus.Metric, logger *slog.Logger) error {
	db := instance.getDB()
	rows, err := db.QueryContext(ctx, slaveThreadsQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	// fields of row
	var threadID, name string
	var state sql.NullString
	for rows.Next() {
		err = rows.Scan(&threadID, &name, &state)
		if err != nil {
			return err
		}

		isBusy := float64(1)
		if !state.Valid || idleThreadStatesRegex.Match([]byte(state.String)) {
			isBusy = 0
		}
		ch <- prometheus.MustNewConstMetric(
			slaveThreadsInfoDesc,
			prometheus.GaugeValue,
			isBusy,
			threadID,
			name[strings.LastIndex(name, "/")+1:],
		)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	return nil
}

// check interface
var _ Scraper = ScrapeSlaveThreads{}
