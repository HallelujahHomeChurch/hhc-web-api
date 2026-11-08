package onlinebulletins

import "testing"

func TestOfficeBodyFontsDoNotRequireUnknownRoleReview(t *testing.T) {
	for _, family := range []string{"BCDEEE+Aptos", "Calibri", "BCEJEE+Helvetica", "MicrosoftJhengHeiRegular", "BCDIEE+MonotypeCorsiva"} {
		if role, known := sourceFontRole(family); !known || role != "body" {
			t.Fatalf("%s: %s %t", family, role, known)
		}
	}
	if _, known := sourceFontRole("BCDHEE+Undefined"); known {
		t.Fatal("unnamed fonts still require human role review")
	}
}

func TestReaderCoverDoesNotRequireExcludedContactOrQRContent(t *testing.T) {
	page := GeometryPage{Number: 1, Images: []GeometryImage{{SHA256: coverGraphicFields[0].sha256, Box: coverGraphicFields[0].box}}}
	slots, issues := coverReaderLogoSlots(page)
	if len(slots) != 1 || slots[0].Element != "logo" || len(issues) != 0 {
		t.Fatal(slots, issues)
	}
	page.Images = nil
	if _, issues = coverReaderLogoSlots(page); len(issues) != 1 {
		t.Fatal("missing visible logo must remain blocking")
	}
	image := GeometryImage{SHA256: coverGraphicFields[1].sha256, Box: coverGraphicFields[1].box}
	image.Box.Y += .037
	if !isExcludedCoverQR(image) {
		t.Fatal("moved known footer QR should not block omitted content")
	}
	image.Box.Y = .3
	if isExcludedCoverQR(image) {
		t.Fatal("body image must not be treated as omitted footer")
	}
	image.Box.Y = .89
	image.SHA256 = "unknown"
	if isExcludedCoverQR(image) {
		t.Fatal("unknown image must remain reviewable")
	}
}

func TestNativeMastheadRequiresReviewedSourceAnchorAndNeverCopiesItsPixels(t *testing.T) {
	page := GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height, Images: []GeometryImage{{SHA256: "a00d0e18ecaa51e26fe18e88d5550ca4a16091729a8573b267b04a9bc7e47803", Box: Box{X: .345696, Y: .079247, Width: .429853, Height: .019456}}}}
	slot, issues := coverMastheadSlot(page)
	if slot == nil || len(issues) != 0 || slot.Element != "masthead" || slot.Style.FontSize != 28 || slot.Box.Width*templateV1Width < 245 {
		t.Fatal("native masthead placement missing")
	}
	page.Images = append(page.Images, page.Images[0])
	if slot, issues = coverMastheadSlot(page); slot != nil || len(issues) != 1 || !issues[0].Blocking {
		t.Fatal("duplicate raster anchor silently accepted")
	}
	page.Images = page.Images[:1]
	page.Images[0].Box.Y = .2
	if slot, issues = coverMastheadSlot(page); slot != nil || len(issues) != 1 {
		t.Fatal("moved source raster silently assigned original native masthead")
	}
}

func TestCoverFooterUsesCompleteVerifiedFixedTextAndKnownGraphics(t *testing.T) {
	page := GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height}
	for _, field := range []struct {
		left, right float64
		lines       []string
	}{
		{0, .42, []string{"教會地址：臺北市仁愛路三段29號B1", "Address:No.29,B1,Sec3,Ren-Ai.Rd,Taipei,Taiwan", "電子郵件 (E-mail)：home.church@msa.hinet.net", "教會網址 (Website)：www.alive.org.tw", "電話 (Phone)：(02) 2776-1157 / 8773-1972", "傳真 (Fax)：(02) 2781-4980"}},
		{.42, .51, []string{"家教會官網"}}, {.51, .60, []string{"家教會", "YOUTUBE"}}, {.60, .70, []string{"神國大樂", "京成兄妹"}},
		{.70, 1, []string{"歡迎使用您的手機掃描", "左方的QRCODE圖樣", "可以立即加入家教會官網", "直播/下載週報/福音餐會"}},
	} {
		for index, text := range field.lines {
			page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "DFXingShu", FontSize: 8, Box: Box{X: field.left + .005, Y: .865 + float64(index)*.012, Width: field.right - field.left - .01, Height: 8 / templateV1Height}})
		}
	}
	page.Images = []GeometryImage{
		{SHA256: "2b1f2ed34df0edd7f3217a97312ce42190c923742ac7eb5ab56a7afb3b582161", Box: Box{X: .128166, Y: .035633, Width: .157226, Height: .111174}},
		{SHA256: "4c5bfab9ff5ffb6c60ca78ab6331950842ea507e568b99afea5293441535e46f", Box: Box{X: .432708, Y: .858573, Width: .079369, Height: .055706}},
		{SHA256: "8f081fe5c5b7413390cd8de52259f852d1257b25bf91c883e5a3979310f918e0", Box: Box{X: .532571, Y: .862433, Width: .063327, Height: .046620}},
		{SHA256: "d87db04790d600818b33ff1b6e7268b97292dc8ed1635e347e1ac3f02e19b9df", Box: Box{X: .623614, Y: .861899, Width: .066435, Height: .046976}},
	}
	slots, issues, err := coverFooterSlots(page)
	if err != nil || len(issues) != 0 || len(slots) != 9 {
		t.Fatalf("fixed footer: slots=%d issues=%v err=%v", len(slots), issues, err)
	}
	if slots[7].Element != "youtubeQR" || slots[7].Box.X != .532571 || slots[8].Element != "streamQR" || slots[8].Box.X != .623614 {
		t.Fatal("QR columns were switched")
	}
	page.Texts[0].Text = "不同的固定聯絡文字"
	page.Images[0].SHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	slots, issues, err = coverFooterSlots(page)
	if err != nil || len(slots) != 7 || len(issues) != 2 {
		t.Fatal("unverified fixed text/image was silently substituted")
	}
	for _, issue := range issues {
		if !issue.Blocking || issue.Code != "fixed_template_source_mismatch" {
			t.Fatal("footer mismatch must block review")
		}
	}
}

func TestCoverChurchLabelsKeepTheirSeparateSourceRows(t *testing.T) {
	page := GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height}
	for _, field := range []struct {
		text string
		x, y float64
	}{
		{"異象使命：華人遍地興起、福音傳到地極", .445, .156},
		{"團契行動：共同生活、愛與成全、恩膏傳承", .478, .175},
		{"堅持理想：宣教主導、靈恩神學、團隊事奉、門徒訓練", .512, .194},
		{"主任牧師：曾英欽 牧師", .096, .208},
		{"本週靈糧主題", .077, .245},
	} {
		page.Texts = append(page.Texts, GeometryText{Text: field.text, FontFamily: "DFXingShu", FontSize: 10, Box: Box{X: field.x, Y: field.y, Width: .40, Height: 10 / templateV1Height}})
	}
	slots, issues, err := coverChurchSlots(page)
	if err != nil || len(slots) != 5 || len(issues) != 0 {
		t.Fatalf("fixed church profile: %v %v", err, issues)
	}
	if slots[0].Element != "visionMission" || slots[1].Box.X <= slots[0].Box.X || slots[2].Box.X <= slots[1].Box.X || slots[0].Style.FontSize != 10 {
		t.Fatal("source vision staircase was flattened into one left-aligned paragraph")
	}
	page.Texts[0].Text = "異象使命：遍地華人興起、福音傳到地極"
	slots, issues, err = coverChurchSlots(page)
	if err != nil || len(slots) != 5 || len(issues) != 1 || issues[0].Code != "fixed_template_text_variant" || !issues[0].Blocking {
		t.Fatal("known variant must retain layout but require explicit review", issues, err)
	}
	page.Texts[0].Text = "新增不同的固定文字"
	_, issues, err = coverChurchSlots(page)
	if err != nil || len(issues) != 1 || !issues[0].Blocking || issues[0].Code != "fixed_template_source_mismatch" {
		t.Fatal("changed fixed source was silently replaced with old church text")
	}
}
