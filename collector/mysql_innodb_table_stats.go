// Copyright 2025 Shattered Silicon
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Scrape `mysql.innodb_table_stats`.

package collector

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	innodbTableStatsQuery = `
		  SELECT
		    database_name AS TABLE_SCHEMA,
		    table_name AS TABLE_NAME,
		    n_rows AS TABLE_ROWS,
		    clustered_index_size * 16 * 1024 AS DATA_LENGTH_BYTES,
		    sum_of_other_index_sizes * 16 * 1024 AS INDEX_LENGTH_BYTES
		  FROM mysql.innodb_table_stats
		  WHERE database_name not in ('mysql', 'information_schema', 'performance_schema')
		`

	innodbTableCountQuery = `
		SELECT COUNT(1)
		FROM mysql.innodb_table_stats;
	`
)

// Metric descriptors.
var (
	mysqlInnoDBTableStatsRowsDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, mysqlSubsystem, "innodb_table_rows"),
		"The estimated number of rows in the table from mysql.innodb_table_stats",
		[]string{"schema", "table"}, nil,
	)
	mysqlInnoDBTableStatsSizeDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, mysqlSubsystem, "innodb_table_size"),
		"The size of the table components from mysql.innodb_table_stats",
		[]string{"schema", "table", "component"}, nil,
	)
	innodbTablesCountDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, mysqlSubsystem, "innodb_table_count"),
		"The count of the table components from mysql.innodb_table_stats",
		nil, nil,
	)
)

// ScrapeInnoDBTableStats collects from `mysql.innodb_table_stats`.
type ScrapeInnoDBTableStats struct{}

// Name of the Scraper. Should be unique.
func (ScrapeInnoDBTableStats) Name() string {
	return mysqlSubsystem + ".innodb_table_stats"
}

// Help describes the role of the Scraper.
func (ScrapeInnoDBTableStats) Help() string {
	return "Collect data from mysql.innodb_table_stats"
}

// Version of MySQL from which scraper is available.
func (ScrapeInnoDBTableStats) Version() float64 {
	return 5.1
}

// Scrape collects data from database connection and sends it over channel as prometheus metric.
func (ScrapeInnoDBTableStats) Scrape(ctx context.Context, instance *instance, ch chan<- prometheus.Metric, logger *slog.Logger) error {
	db := instance.getDB()

	rows, err := db.QueryContext(ctx, innodbTableStatsQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		tableSchema string
		tableName   string
		tableRows   uint64
		dataLength  uint64
		indexLength uint64
	)

	for rows.Next() {
		err = rows.Scan(
			&tableSchema,
			&tableName,
			&tableRows,
			&dataLength,
			&indexLength,
		)
		if err != nil {
			return err
		}

		ch <- prometheus.MustNewConstMetric(
			mysqlInnoDBTableStatsRowsDesc, prometheus.GaugeValue, float64(tableRows),
			tableSchema, tableName,
		)
		ch <- prometheus.MustNewConstMetric(
			mysqlInnoDBTableStatsSizeDesc, prometheus.GaugeValue, float64(dataLength),
			tableSchema, tableName, "data_length",
		)
		ch <- prometheus.MustNewConstMetric(
			mysqlInnoDBTableStatsSizeDesc, prometheus.GaugeValue, float64(indexLength),
			tableSchema, tableName, "index_length",
		)
	}

	var tableCount int
	err = db.QueryRowContext(ctx, innodbTableCountQuery).Scan(&tableCount)
	if err != nil {
		return err
	}

	ch <- prometheus.MustNewConstMetric(innodbTablesCountDesc, prometheus.GaugeValue, float64(tableCount))
	return nil
}
