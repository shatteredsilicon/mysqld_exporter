// Scrape `information_schema.table_statistics`.

package collector

import (
	"context"
	"log/slog"

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
func (ScrapeIndexStat) Scrape(ctx context.Context, instance *instance, ch chan<- prometheus.Metric, logger *slog.Logger) error {
	var varName, varVal string
	err := instance.db.QueryRowContext(ctx, userstatCheckQuery).Scan(&varName, &varVal)
	if err != nil {
		logger.Debug("Detailed index stats are not available:", err)
		return nil
	}
	if varVal == "OFF" {
		logger.Debug("MySQL @@%s is OFF.", varName)
		return nil
	}

	informationSchemaIndexStatisticsRows, err := instance.db.QueryContext(ctx, indexStatQuery)
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
