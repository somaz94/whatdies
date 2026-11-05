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
	if _, err := parseStateful([]byte("stateful: [")); err == nil {
		t.Fatal("want error")
	}
}
