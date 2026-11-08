package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func TestBulletinEntitlementUsesExactSeriesAndLocale(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT entitlement_code FROM hhc_web.bulletin_entitlement_policy WHERE series=\\$1 AND locale=\\$2").
		WithArgs("general", "zh-Hans").
		WillReturnRows(sqlmock.NewRows([]string{"entitlement_code"}).AddRow("bulletin.general.zh-Hans.access"))

	code, err := New(db).BulletinEntitlement(context.Background(), "general", "zh-Hans")
	if err != nil || code != "bulletin.general.zh-Hans.access" {
		t.Fatalf("code=%q err=%v", code, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBulletinEntitlementRejectsUnknownPolicy(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT entitlement_code FROM hhc_web.bulletin_entitlement_policy WHERE series=\\$1 AND locale=\\$2").
		WithArgs("children", "zh-Hant").
		WillReturnRows(sqlmock.NewRows([]string{"entitlement_code"}))

	_, err = New(db).BulletinEntitlement(context.Background(), "children", "zh-Hant")
	if !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
