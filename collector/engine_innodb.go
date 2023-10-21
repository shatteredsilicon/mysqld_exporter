// Scrape `SHOW ENGINE INNODB STATUS`.

package collector

import (
	"context"
	"database/sql"
	"encoding/json"
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
	aioPendingRWs, _ := regexp.Compile(`^Pending normal aio reads:\s?(\d+)?\s(\[\d+(?:, \d+)*\])?\s?, aio writes:\s?(\d+)?\s?(\[\d+(?:, \d+)*\])?`)
	pendingRWs, _ := regexp.Compile(`^(\d+) pending reads, (\d+) pending writes`)

	pendingReads, pendingWrites := 0, 0
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
		} else if data := aioPendingRWs.FindStringSubmatch(line); data != nil {
			var reads, writes int
			if data[1] != "" {
				// format in "Pending normal aio reads: 0 [0, 0, 0, 0] , aio writes: 0 [0, 0, 0, 0]"
				// or in "Pending normal aio reads: 0 , aio writes: 0"
				reads, _ = strconv.Atoi(data[1])
				writes, _ = strconv.Atoi(data[3])
			} else {
				// format in "Pending normal aio reads: [0, 0, 0, 0] , aio writes: [0, 0, 0, 0]"
				var readSlice, writeSlice []int
				json.Unmarshal([]byte(data[2]), &readSlice)
				json.Unmarshal([]byte(data[4]), &writeSlice)

				for i := range readSlice {
					reads += readSlice[i]
				}
				for i := range writeSlice {
					writes += writeSlice[i]
				}
			}

			pendingReads += reads
			pendingWrites += writes
		} else if data := pendingRWs.FindStringSubmatch(line); data != nil {
			reads, _ := strconv.Atoi(data[1])
			writes, _ := strconv.Atoi(data[2])

			pendingReads += reads
			pendingWrites += writes
		}
	}

	ch <- prometheus.MustNewConstMetric(
		newDesc(innodb, "pending_reads", "InnoDB pending reads."),
		prometheus.GaugeValue,
		float64(pendingReads),
	)

	ch <- prometheus.MustNewConstMetric(
		newDesc(innodb, "pending_writes", "InnoDB pending writes."),
		prometheus.GaugeValue,
		float64(pendingWrites),
	)

	return nil
}
