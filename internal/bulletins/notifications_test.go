package bulletins

import "testing"

func TestBuildNotificationKeepsSeriesIndependent(t *testing.T) {
	number := 1300
	issue := Issue{IssueNumber: &number, Versions: []Version{
		{Series: "general", Locale: "zh-Hant", Title: "一般週報", Status: "published"},
		{Series: "children", Locale: "zh-Hant", Title: "兒童週報", Status: "published"},
		{Series: "children", Locale: "en", Title: "Children", Status: "published"},
	}}
	preview, err := BuildNotification(issue, "children")
	if err != nil {
		t.Fatal(err)
	}
	translations := preview.Notifications[0].Translations
	if preview.Name != "Children's weekly bulletin 1300" || len(translations) != 2 || translations["zh-Hant"].Body != "兒童週報" || translations["en"].ActionURL != "/en/literature-ministry?series=children" || preview.Notifications[1].RequiredEntitlements["en"] != "bulletin.children.en.access" {
		t.Fatalf("preview=%#v", preview)
	}
}

func TestBuildNotificationDefaultsLegacySeriesToGeneral(t *testing.T) {
	issue := Issue{Versions: []Version{{Locale: "en", Title: "Weekly", Status: "published"}}}
	preview, err := BuildNotification(issue, "")
	if err != nil || preview.Series != "general" || len(preview.Notifications[0].Translations) != 3 {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
}
