package destination

import "testing"

// ---- PortNum ----

func TestPortNum_Valid(t *testing.T) {
	cases := []struct {
		port string
		want uint16
	}{
		{"22", 22},
		{"8080", 8080},
		{"65535", 65535},
		{"0", 0},
	}
	for _, tc := range cases {
		d := &Destination{Port: tc.port}
		got, err := d.PortNum()
		if err != nil {
			t.Errorf("PortNum(%q) unexpected error: %v", tc.port, err)
			continue
		}
		if got != tc.want {
			t.Errorf("PortNum(%q) = %d, want %d", tc.port, got, tc.want)
		}
	}
}

func TestPortNum_Invalid(t *testing.T) {
	cases := []string{"", "notaport", "99999", "-1", "22.5"}
	for _, port := range cases {
		d := &Destination{Port: port}
		_, err := d.PortNum()
		if err == nil {
			t.Errorf("PortNum(%q): expected error, got nil", port)
		}
	}
}

// ---- ResolvedUser ----

func TestResolvedUser_TopLevelField(t *testing.T) {
	d := &Destination{User: "alice", Properties: map[string]string{"User": "ignored"}}
	if got := d.ResolvedUser(); got != "alice" {
		t.Errorf("ResolvedUser() = %q, want alice", got)
	}
}

func TestResolvedUser_PropertiesFallback(t *testing.T) {
	d := &Destination{Properties: map[string]string{"User": "bob"}}
	if got := d.ResolvedUser(); got != "bob" {
		t.Errorf("ResolvedUser() = %q, want bob", got)
	}
}

func TestResolvedUser_EmptyWhenUnset(t *testing.T) {
	d := &Destination{}
	if got := d.ResolvedUser(); got != "" {
		t.Errorf("ResolvedUser() = %q, want empty", got)
	}
}

func TestResolvedUser_NilProperties(t *testing.T) {
	d := &Destination{Properties: nil}
	if got := d.ResolvedUser(); got != "" {
		t.Errorf("ResolvedUser() with nil Properties = %q, want empty", got)
	}
}

// ---- ResolvedPassword ----

func TestResolvedPassword_TopLevelField(t *testing.T) {
	d := &Destination{Password: "s3cr3t", Properties: map[string]string{"Password": "ignored"}}
	if got := d.ResolvedPassword(); got != "s3cr3t" {
		t.Errorf("ResolvedPassword() = %q, want s3cr3t", got)
	}
}

func TestResolvedPassword_PropertiesFallback(t *testing.T) {
	d := &Destination{Properties: map[string]string{"Password": "from-props"}}
	if got := d.ResolvedPassword(); got != "from-props" {
		t.Errorf("ResolvedPassword() = %q, want from-props", got)
	}
}

func TestResolvedPassword_EmptyWhenUnset(t *testing.T) {
	d := &Destination{}
	if got := d.ResolvedPassword(); got != "" {
		t.Errorf("ResolvedPassword() = %q, want empty", got)
	}
}
