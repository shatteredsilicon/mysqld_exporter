// Scrape `information_schema.table_statistics`.

package collector

import (
	"context"
	"database/sql"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
)

const indexStatQuery = `
	SELECT
		TABLE_SCHEMA,
		TABLE_NAME,
		INDEX_NAME,
		ROWS_READ
		FROM information_schema.index_statistics
	`

var (
	infoSchemaIndexStatsRowsReadDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, informationSchema, "index_statistics_rows_read_total"),
		"The number of rows read from the index.",
		[]string{"schema", "table", "index"}, nil,
	)
)

// ScrapeIndexStat collects from `information_schema.index_statistics`.
type ScrapeIndexStat struct{}

// Name of the Scraper.
func (ScrapeIndexStat) Name() string {
	return "info_schema.indexstats"
}

// Help returns additional information about Scraper.
func (ScrapeIndexStat) Help() string {
	return "If running with userstat=1, set to true to collect index statistics"
}

// Version of MySQL from which scraper is available.
func (ScrapeIndexStat) Version() float64 {
	return 5.1
}

// Scrape collects data.
func (ScrapeIndexStat) Scrape(ctx context.Context, db *sql.DB, ch chan<- prometheus.Metric, logger log.Logger) error {
	var varName, varVal string
	err := db.QueryRowContext(ctx, userstatCheckQuery).Scan(&varName, &varVal)
	if err != nil {
		level.Debug(logger).Log("Detailed index stats are not available:", err)
		return nil
	}
	if varVal == "OFF" {
		level.Debug(logger).Log("MySQL @@%s is OFF.", varName)
		return nil
	}

	informationSchemaIndexStatisticsRows, err := db.QueryContext(ctx, indexStatQuery)
	if err != nil {
		return err
	}
	defer informationSchemaIndexStatisticsRows.Close()

	var (
		tableSchema string
		tableName   string
		indexName   string
		rowsRead    uint64
	)

	for informationSchemaIndexStatisticsRows.Next() {
		err = informationSchemaIndexStatisticsRows.Scan(
			&tableSchema,
			&tableName,
			&indexName,
			&rowsRead,
		)
		if err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			infoSchemaIndexStatsRowsReadDesc, prometheus.GaugeValue, float64(rowsRead),
			tableSchema, tableName, indexName,
		)
	}
	return nil
}

var _ Scraper = ScrapeIndexStat{}
