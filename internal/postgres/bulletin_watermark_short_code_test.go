package postgres

import (
	"context"
	"database/sql"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/google/uuid"
	"os"
	"strings"
	"testing"
)

func TestWatermarkCodeLengthConstraintAllowsShortAndLegacy(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrations.Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{18, 28, 17, 19, 27, 29} {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(`INSERT INTO hhc_web.bulletin_watermark_receipt
   (id,trace_code,user_id,issue_id,locale,revision,source_asset_id,source_sha256,output_sha256,renderer_version,privacy_notice_version,issued_at,expires_at)
   VALUES($1,$2,$3,$4,'zh-Hant',1,$5,$6,$6,'test','test',now(),now()+interval '8760 hours')`,
			uuid.NewString(), strings.Repeat("0", size), uuid.NewString(), uuid.NewString(), testWatermarkAssetID, strings.Repeat("0", 64))
		_ = tx.Rollback()
		if (err == nil) != (size == 18 || size == 28) {
			t.Fatalf("code length %d: %v", size, err)
		}
	}
}
