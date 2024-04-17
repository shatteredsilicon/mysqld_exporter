package collector

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/smartystreets/goconvey/convey"
	"gopkg.in/DATA-DOG/go-sqlmock.v1"
)

const customQueryCounter = `
experiment_garden:
  query: "SELECT fruit, amount FROM experiment.garden;"
  metrics:
    - fruit:
        usage: "LABEL"
        description: "Fruit names"
    - amount:
        usage: "COUNTER"
        description: "Amount fruits in the garden"

`

func TestScrapeCustomQueriesCounter(t *testing.T) {
	convey.Convey("Custom queries counter", t, func() {

		tmpFileName := createTmpFile(t, customQueryCounter)
		defer os.Remove(tmpFileName)

		err := flag.Set("queries-file-name", tmpFileName)
		if err != nil {
			t.Fatalf("cannot set flag: %s", err)
		}
		flag.Parse()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("error opening a stub database connection: %s", err)
		}
		defer db.Close()

		columns := []string{"fruit", "amount"}
		rows := sqlmock.NewRows(columns).
			AddRow("apple", "10").
			AddRow("cherry", "35").
			AddRow("pear", "42").
			AddRow("plumb", "80")
		mock.ExpectQuery(sanitizeQuery("SELECT fruit, amount FROM experiment.garden;")).WillReturnRows(rows)

		ch := make(chan prometheus.Metric)
		go func() {
			if err = (ScrapeCustomQuery{}).Scrape(context.Background(), db, ch, log.NewNopLogger()); err != nil {
				t.Errorf("error calling function on test: %s", err)
			}
			close(ch)
		}()

		counterExpected := []MetricResult{
			{labels: labelMap{"fruit": "apple"}, value: 10, metricType: dto.MetricType_COUNTER},
			{labels: labelMap{"fruit": "cherry"}, value: 35, metricType: dto.MetricType_COUNTER},
			{labels: labelMap{"fruit": "pear"}, value: 42, metricType: dto.MetricType_COUNTER},
			{labels: labelMap{"fruit": "plumb"}, value: 80, metricType: dto.MetricType_COUNTER},
		}
		convey.Convey("Metrics should be resemble", t, func(c convey.C) {
			for _, expect := range counterExpected {
				got := readMetric(<-ch)
				c.So(got, convey.ShouldResemble, expect)
			}
		})

		// Ensure all SQL queries were executed
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expections: %s", err)
		}
	})
}

const customQueryDuration = `
experiment_garden:
  query: "SELECT fruit, ripen FROM experiment.garden;"
  metrics:
    - fruit:
        usage: "LABEL"
        description: "Fruit names"
    - amount:
        usage: "DURATION"
        description: "Time to become ripe."

`

func TestScrapeCustomQueriesDuration(t *testing.T) {
	convey.Convey("Custom queries duration", t, func() {

		tmpFileName := createTmpFile(t, customQueryDuration)
		defer os.Remove(tmpFileName)

		err := flag.Set("queries-file-name", tmpFileName)
		if err != nil {
			t.Fatalf("cannot set flag: %s", err)
		}
		flag.Parse()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("error opening a stub database connection: %s", err)
		}
		defer db.Close()

		columns := []string{"fruit", "amount"}
		rows := sqlmock.NewRows(columns).
			AddRow("apple", "2592000000").
			AddRow("cherry", "2692000000").
			AddRow("pear", "2792000000").
			AddRow("plumb", "2892000000")
		mock.ExpectQuery(sanitizeQuery("SELECT fruit, ripen FROM experiment.garden;")).WillReturnRows(rows)

		ch := make(chan prometheus.Metric)
		go func() {
			if err = (ScrapeCustomQuery{}).Scrape(context.Background(), db, ch, log.NewNopLogger()); err != nil {
				t.Errorf("error calling function on test: %s", err)
			}
			close(ch)
		}()

		counterExpected := []MetricResult{
			{labels: labelMap{"fruit": "apple"}, value: 2592000000, metricType: dto.MetricType_GAUGE},
			{labels: labelMap{"fruit": "cherry"}, value: 2692000000, metricType: dto.MetricType_GAUGE},
			{labels: labelMap{"fruit": "pear"}, value: 2792000000, metricType: dto.MetricType_GAUGE},
			{labels: labelMap{"fruit": "plumb"}, value: 2892000000, metricType: dto.MetricType_GAUGE},
		}
		convey.Convey("Metrics should be resemble", t, func(c convey.C) {
			for _, expect := range counterExpected {
				got := readMetric(<-ch)
				c.So(got, convey.ShouldResemble, expect)
			}
		})

		// Ensure all SQL queries were executed
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expections: %s", err)
		}
	})
}

const customQueryNoDb = `
experiment_garden:
  query: "SELECT fruit, ripen FROM non_existed_experiment.garden;"
  metrics:
    - fruit:
        usage: "LABEL"
        description: "Fruit names"
    - amount:
        usage: "DURATION"
        description: "Time to become ripe."

`

func TestScrapeCustomQueriesDbError(t *testing.T) {
	convey.Convey("Custom queries db error", t, func() {

		tmpFileName := createTmpFile(t, customQueryNoDb)
		defer os.Remove(tmpFileName)

		err := flag.Set("queries-file-name", tmpFileName)
		if err != nil {
			t.Fatalf("cannot set flag: %s", err)
		}
		flag.Parse()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("error opening a stub database connection: %s", err)
		}
		defer db.Close()

		expectedError := fmt.Errorf("ERROR 1049 (42000): Unknown database 'non_existed_experiment'")
		mock.ExpectQuery(sanitizeQuery("SELECT fruit, ripen FROM non_existed_experiment.garden;")).WillReturnError(expectedError)

		ch := make(chan prometheus.Metric)

		expectedErr := "experiment_garden:error running query on database: experiment_garden, ERROR 1049 (42000): Unknown database 'non_existed_experiment'"
		convey.Convey("Should raise error ", t, func(c convey.C) {
			err = (ScrapeCustomQuery{}).Scrape(context.Background(), db, ch, log.NewNopLogger())
			c.So(err, convey.ShouldBeError, expectedErr)
		})
		close(ch)
	})
}

const customQueryIncorrectYaml = `
{"foo": "bar"}
`

func TestScrapeCustomQueriesIncorrectYaml(t *testing.T) {
	convey.Convey("Custom queries incorrect yaml", t, func() {

		tmpFileName := createTmpFile(t, customQueryIncorrectYaml)
		defer os.Remove(tmpFileName)

		err := flag.Set("queries-file-name", tmpFileName)
		if err != nil {
			t.Fatalf("cannot set flag: %s", err)
		}
		flag.Parse()
		db, _, err := sqlmock.New()
		if err != nil {
			t.Fatalf("error opening a stub database connection: %s", err)
		}
		defer db.Close()

		ch := make(chan prometheus.Metric)

		convey.Convey("Should raise error ", t, func(c convey.C) {
			err = (ScrapeCustomQuery{}).Scrape(context.Background(), db, ch, log.NewNopLogger())
			c.So(err, convey.ShouldBeError, "failed to add custom queries:incorrect yaml format for bar")
		})
		close(ch)

	})
}

func TestScrapeCustomQueriesNoFile(t *testing.T) {
	convey.Convey("Passed as a custom queries unexisted file or path", t, func(c convey.C) {
		err := flag.Set("queries-file-name", "/wrong/path/custom_query_test.yaml")
		if err != nil {
			t.Fatalf("cannot set flag: %s", err)
		}
		flag.Parse()
		db, _, err := sqlmock.New()
		if err != nil {
			t.Fatalf("error opening a stub database connection: %s", err)
		}
		ch := make(chan prometheus.Metric)
		err = (ScrapeCustomQuery{}).Scrape(context.Background(), db, ch, log.NewNopLogger())
		close(ch)
		c.So(err, convey.ShouldBeError, "failed to open custom queries:open /wrong/path/custom_query_test.yaml: no such file or directory")
	})
}

func createTmpFile(t *testing.T, content string) string {
	// Create our Temp File
	tmpFile, err := ioutil.TempFile("", "custom_queries.*.yaml")
	if err != nil {
		t.Fatalf("Cannot create temporary file: %s", err)
	}

	// Example writing to the file
	_, err = tmpFile.Write([]byte(content))
	if err != nil {
		t.Fatalf("Failed to write to temporary file: %s", err)
	}
	return tmpFile.Name()
}
