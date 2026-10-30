package agy

import "testing"

func TestParseReportKeepsModelFamiliesApart(t *testing.T) {
	stdout := `{"status":"SUCCESS","command":{"data":{"groups":[
		{"name":"Gemini Models","buckets":[
			{"id":"gemini-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":1,"reset_time":"2026-10-30T18:26:43Z"}]},
		{"name":"Claude and GPT models","buckets":[
			{"id":"3p-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":0,"reset_time":"2026-10-30T18:26:43Z"},
			{"id":"3p-weekly","name":"Weekly Limit Remaining","window":"weekly","reset_time":"bad"}]}]}}}`
	windows, err := parseReport(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	if *windows[1].UsedPercent != 100 || *windows[1].RemainingPercent != 0 {
		t.Fatalf("measured exhaustion must survive: %+v", windows[1])
	}
	if windows[0].ID != "Gemini Models/gemini-5h" {
		t.Fatalf("group key lost: %+v", windows[0])
	}
}

func TestParseReportRejectsFailedStatus(t *testing.T) {
	if _, err := parseReport(`{"status":"ERROR"}`); err == nil {
		t.Fatal("want error for non-success status")
	}
}
