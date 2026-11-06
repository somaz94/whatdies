package registry

import "testing"

func TestStatefulIncludesTheTypesFromRealIncidents(t *testing.T) {
	types, err := Stateful()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"aws_db_instance", "aws_s3_bucket", "google_sql_database_instance"} {
		if !types[want] {
			t.Errorf("%s missing", want)
		}
	}
	if types["terraform_data"] {
		t.Error("terraform_data holds no data and must not be listed")
	}
}

func TestParseStatefulRejectsBrokenYAML(t *testing.T) {
	for _, doc := range []string{"stateful: [", "statefull:\n  - x\n"} {
		if _, err := parseStateful([]byte(doc)); err == nil {
			t.Errorf("%q: want error", doc)
		}
	}
}

func TestGuardsNameTheFlagsBehindRealDataLoss(t *testing.T) {
	guards, err := Guards()
	if err != nil {
		t.Fatal(err)
	}
	if off, ok := guards["aws_s3_bucket"]["force_destroy"]; !ok || !off {
		t.Error("aws_s3_bucket force_destroy = true must be listed")
	}
	if off, ok := guards["aws_db_instance"]["deletion_protection"]; !ok || off {
		t.Error("aws_db_instance deletion_protection = false must be listed")
	}
}

func TestParseGuardsRejectsValuesThatAreNotBooleans(t *testing.T) {
	for _, doc := range []string{"guards: [", "guards:\n  x:\n    y: maybe\n", "guards:\n  x: [a]\n", "guard:\n  x:\n    y: true\n"} {
		if _, err := parseGuards([]byte(doc)); err == nil {
			t.Errorf("%q: want error", doc)
		}
	}
}
