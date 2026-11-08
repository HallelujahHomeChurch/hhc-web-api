package main

import (
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/config"
)

func TestDatabaseGateSeparatesJobsFromRollbackApp(t *testing.T) {
	for _, test := range []struct {
		name, marker string
		once, job    bool
	}{
		{name: "rollback-app"},
		{name: "local-once", once: true},
		{name: "job-preflight", job: true, marker: "hhc-web-bulletin-watermark"},
		{name: "empty-job-marker-fails-closed", job: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CONTAINER_APP_JOB_NAME", test.marker)
			if !test.job {
				if err := os.Unsetenv("CONTAINER_APP_JOB_NAME"); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.Config{DatabaseURL: "postgres://fixture:fixture@127.0.0.1:1/fixture?sslmode=disable", DBMaxOpenConns: 2, DBMaxIdleConns: 1, DBConnMaxLifetime: time.Minute}
			db, err := openDatabase(cfg, test.once)
			if test.once || test.job {
				if err == nil || db != nil {
					t.Fatal("Job skipped authenticated login gate")
				}
				return
			}
			if err != nil || db == nil {
				t.Fatalf("rollback App unexpectedly requires the Job login: %v", err)
			}
			defer db.Close()
			if db.Stats().MaxOpenConnections != 2 {
				t.Fatal("rollback App pool changed")
			}
		})
	}
}
