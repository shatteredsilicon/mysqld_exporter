// Scrape `SHOW ENGINE INNODB STATUS`.

package collector

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Subsystem.
	innodb = "engine_innodb"
	// Query.
	engineInnodbStatusQuery = `SHOW ENGINE INNODB STATUS`
)

// ScrapeEngineInnodbStatus scrapes from `SHOW ENGINE INNODB STATUS`.
type ScrapeEngineInnodbStatus struct{}

// Name of the Scraper.
func (ScrapeEngineInnodbStatus) Name() string {
	return "engine_innodb_status"
}

// Help returns additional information about Scraper.
func (ScrapeEngineInnodbStatus) Help() string {
	return "Collect from SHOW ENGINE INNODB STATUS"
}

// Version of MySQL from which scraper is available.
func (ScrapeEngineInnodbStatus) Version() float64 {
	return 5.1
}

// Scrape collects data.
func (ScrapeEngineInnodbStatus) Scrape(ctx context.Context, db *sql.DB, ch chan<- prometheus.Metric) error {
	rows, err := db.QueryContext(ctx, engineInnodbStatusQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	var typeCol, nameCol, statusCol string
	// First row should contain the necessary info. If many rows returned then it's unknown case.
	if rows.Next() {
		if err := rows.Scan(&typeCol, &nameCol, &statusCol); err != nil {
			return err
		}
	}

	// 0 queries inside InnoDB, 0 queries in queue
	// 0 read views open inside InnoDB
	rQueries, _ := regexp.Compile(`(\d+) queries inside InnoDB, (\d+) queries in queue`)
	rViews, _ := regexp.Compile(`(\d+) read views open inside InnoDB`)
	ioThreads, _ := regexp.Compile(`^Pending normal aio reads: \[([^\[]+)\] , aio writes: \[([^\[]+)\]`)

	for _, line := range strings.Split(statusCol, "\n") {
		if data := rQueries.FindStringSubmatch(line); data != nil {
			value, _ := strconv.ParseFloat(data[1], 64)
			ch <- prometheus.MustNewConstMetric(
				newDesc(innodb, "queries_inside_innodb", "Queries inside InnoDB."),
				prometheus.GaugeValue,
				value,
			)
			value, _ = strconv.ParseFloat(data[2], 64)
			ch <- prometheus.MustNewConstMetric(
				newDesc(innodb, "queries_in_queue", "Queries in queue."),
				prometheus.GaugeValue,
				value,
			)
		} else if data := rViews.FindStringSubmatch(line); data != nil {
			value, _ := strconv.ParseFloat(data[1], 64)
			ch <- prometheus.MustNewConstMetric(
				newDesc(innodb, "read_views_open_inside_innodb", "Read views open inside InnoDB."),
				prometheus.GaugeValue,
				value,
			)
		} else if data := ioThreads.FindStringSubmatch(line); data != nil {
			rThreads := strings.Split(data[1], ",")
			for i, t := range rThreads {
				copyI := i
				pending, _ := strconv.ParseFloat(t, 64)
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(
						prometheus.BuildFQName(namespace, innodb, "pending_normal_aio_reads"),
						"InnoDB ending normal aio reads.",
						[]string{"index"},
						nil,
					),
					prometheus.GaugeValue,
					float64(pending),
					strconv.Itoa(copyI),
				)
			}

			wThreads := strings.Split(data[2], ",")
			for i, t := range wThreads {
				copyI := i
				pending, _ := strconv.ParseFloat(t, 64)
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(
						prometheus.BuildFQName(namespace, innodb, "pending_normal_aio_writes"),
						"InnoDB ending normal aio writes.",
						[]string{"index"},
						nil,
					),
					prometheus.GaugeValue,
					float64(pending),
					strconv.Itoa(copyI),
				)
			}
		}
	}

	return nil
}
