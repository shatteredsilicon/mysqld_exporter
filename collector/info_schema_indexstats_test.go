package collector

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/promslog"
	"github.com/smartystreets/goconvey/convey"
	"gopkg.in/DATA-DOG/go-sqlmock.v1"
)

func TestScrapeIndexStat(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("error opening a stub database connection: %s", err)
	}
	defer db.Close()

	mock.ExpectQuery(sanitizeQuery(userstatCheckQuery)).WillReturnRows(sqlmock.NewRows([]string{"Variable_name", "Value"}).
		AddRow("userstat", "ON"))

	columns := []string{"TABLE_SCHEMA", "TABLE_NAME", "INDEX_NAME", "ROWS_READ"}
	rows := sqlmock.NewRows(columns).
		AddRow("mysql", "db", "db_index", 238).
		AddRow("mysql", "proxies_priv", "proxies_priv_index", 99).
		AddRow("mysql", "user", "user_index", 1064)
	mock.ExpectQuery(sanitizeQuery(indexStatQuery)).WillReturnRows(rows)

	ch := make(chan prometheus.Metric)
	go func() {
		if err = (ScrapeIndexStat{}).Scrape(context.Background(), &instance{db: db}, ch, promslog.NewNopLogger()); err != nil {
			t.Errorf("error calling function on test: %s", err)
		}
		close(ch)
	}()

	expected := []MetricResult{
		{labels: labelMap{"schema": "mysql", "table": "db", "index": "db_index"}, value: 238},
		{labels: labelMap{"schema": "mysql", "table": "proxies_priv", "index": "proxies_priv_index"}, value: 99},
		{labels: labelMap{"schema": "mysql", "table": "user", "index": "user_index"}, value: 1064},
	}
	convey.Convey("Metrics comparison", t, func(c convey.C) {
		for _, expect := range expected {
			got := readMetric(<-ch)
			c.So(expect, convey.ShouldResemble, got)
		}
	})

	// Ensure all SQL queries were executed
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expections: %s", err)
	}
}
